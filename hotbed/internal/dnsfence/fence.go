package dnsfence

// The egress-allowlist DNS fence.
//
// Sandboxes under an `allowlist` egress preset iptables-DNAT all their :53
// traffic to the docker bridge gateway on Proxy.DNSPort (default 15353).
// This listener sits there and arbitrates every lookup:
//
//   - source IP attributed to a sandbox (state-dir ip map), qname allowed
//     by that sandbox's list → forwarded upstream, IPv4 answers parsed and
//     appended to the state-dir event log (the daemon installs the matching
//     iptables ACCEPT inside the sandbox netns seconds later),
//   - anything else → synthesized REFUSED (fail closed).
//
// It runs inside the docker engine netns — on Docker Desktop as part of the
// proxy relay (`hotbedd proxy`), on native engines as part of `hotbedd
// serve` — because containers cannot reach the host the other way round.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"hotbed/internal/config"
	"hotbed/internal/env"
)

// State file names inside the shared state dir.
const (
	IPsFile    = "egress-ips.json"     // {ip: sandbox-name}, daemon-written
	EventsFile = "egress-events.jsonl" // {sandbox,ip} fence hits, fence-written
)

// IPsPath / EventsPath locate the fence state files.
func IPsPath() string    { return filepath.Join(env.StateDir(), IPsFile) }
func EventsPath() string { return filepath.Join(env.StateDir(), EventsFile) }

// ipSnapshot is the daemon-written {ip: sandbox-name} snapshot file format.
type ipSnapshot struct {
	IPs map[string]string `json:"ips"`
}

// event is one resolved-domain hit, consumed by the daemon's egress keeper.
type event struct {
	Sandbox string `json:"sandbox"`
	IP      string `json:"ip"`
}

type fence struct {
	upstream string
	load     func() (*config.Config, error)

	ipMu   sync.Mutex
	ipStat fileStat
	ipMap  map[string]string

	cfgMu   sync.Mutex
	cfgStat fileStat
	cfg     *config.Config

	seenMu sync.Mutex
	seen   map[string]struct{} // event dedup: one log line per (sandbox, ip)
}

// fileStat is the cheap freshness stamp for cached state reads.
type fileStat struct {
	mtime time.Time
	size  int64
	have  bool
}

func stampOf(path string) fileStat {
	fi, err := os.Stat(path)
	if err != nil {
		return fileStat{}
	}
	return fileStat{mtime: fi.ModTime(), size: fi.Size(), have: true}
}

func (s fileStat) fresh(other fileStat) bool {
	return s.have == other.have && s.mtime.Equal(other.mtime) && s.size == other.size
}

// Run starts the fence listeners (UDP + TCP) on bind:port and returns when
// one dies terminally. A busy port (another daemon or suite relay already
// owns the fence) surfaces as a bind error — callers log-skip it instead of
// failing boot: the fence is a single-winner listener by design.
func Run(bind, port string) error {
	upstream := os.Getenv("HOTBEDD_DNS_UPSTREAM")
	if upstream == "" {
		upstream = "1.1.1.1:53"
	}
	f := &fence{
		upstream: upstream,
		load:     config.Load,
		seen:     map[string]struct{}{},
	}
	addr := net.JoinHostPort(bind, port)
	u, err := net.ListenPacket("udp", addr)
	if err != nil {
		return fmt.Errorf("dns fence udp %s: %w", addr, err)
	}
	defer u.Close()
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("dns fence tcp %s: %w", addr, err)
	}
	defer l.Close()

	errc := make(chan error, 2)
	go func() {
		buf := make([]byte, 64*1024)
		for {
			n, src, err := u.ReadFrom(buf)
			if err != nil {
				errc <- err
				return
			}
			if n > 16 && n < len(buf) {
				go f.onPacket(u, src, buf[:n:n])
			}
		}
	}()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				errc <- err
				return
			}
			go f.serveTCP(c)
		}
	}()
	return <-errc
}

// onPacket answers one UDP datagram.
func (f *fence) onPacket(pc net.PacketConn, src net.Addr, pkt []byte) {
	resp, sb, ips := f.arbitrate(src, pkt)
	if resp != nil {
		if _, err := pc.WriteTo(resp, src); err != nil {
			_ = err // unreplyable client (vanished netns): nothing to do
		}
	}
	f.log(sb, ips)
}

// arbitrate builds the response for one raw query, attributing the source
// to a named sandbox; also returns the resolved IPv4 blobs (nil, nil for
// undecodable input).
func (f *fence) arbitrate(src net.Addr, raw []byte) ([]byte, string, [][]byte) {
	q, ok := parseQuery(raw)
	if !ok {
		return nil, "", nil
	}
	sb := f.sandboxOf(hostOf(src))
	if sb == "" || !f.allowed(sb, q.domain) {
		return q.refusedA(), "", nil
	}
	resp, err := f.exchange(raw)
	if err != nil {
		return q.noRecords(), sb, nil // upstream hiccup: empty NOERROR, client retries
	}
	ips := extractIPv4(resp)
	if out := q.withA(ips); out != nil {
		return out, sb, ips
	}
	return resp, sb, nil
}

// serveTCP answers length-prefixed DNS-over-TCP (large answers, pipelining).
func (f *fence) serveTCP(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(16 * time.Second))
	r := bufio.NewReader(c)
	for {
		h := make([]byte, 2)
		if _, err := io.ReadFull(r, h); err != nil {
			return
		}
		n := int(h[0])<<8 | int(h[1])
		if n < 17 || n > 64*1024 {
			return
		}
		raw := make([]byte, n)
		if _, err := io.ReadFull(r, raw); err != nil {
			return
		}
		resp, sb, ips := f.arbitrate(c.RemoteAddr(), raw)
		if resp == nil {
			return
		}
		out := []byte{byte(len(resp) >> 8), byte(len(resp))}
		out = append(out, resp...)
		if _, err := c.Write(out); err != nil {
			return
		}
		f.log(sb, ips)
	}
}

// exchange forwards one raw query upstream and returns the raw response.
// UDP first; TCP fallback covers responses that would truncate.
func (f *fence) exchange(raw []byte) ([]byte, error) {
	resp, err := f.exchangeUDP(raw)
	if err == nil {
		return resp, nil
	}
	return f.exchangeTCP(raw)
}

func (f *fence) exchangeUDP(raw []byte) ([]byte, error) {
	c, err := net.DialTimeout("udp", f.upstream, 3*time.Second)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(4 * time.Second))
	if _, err := c.Write(raw); err != nil {
		return nil, err
	}
	buf := make([]byte, 64*1024)
	n, err := c.Read(buf)
	if err != nil {
		return nil, err
	}
	resp := buf[:n:n]
	if idOf(raw) != idOf(resp) {
		return nil, fmt.Errorf("upstream transaction id mismatch")
	}
	return resp, nil
}

func (f *fence) exchangeTCP(raw []byte) ([]byte, error) {
	d := net.Dialer{Timeout: 3 * time.Second}
	c, err := d.Dial("tcp", f.upstream)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(4 * time.Second))
	frame := []byte{byte(len(raw) >> 8), byte(len(raw))}
	frame = append(frame, raw...)
	if _, err := c.Write(frame); err != nil {
		return nil, err
	}
	h := make([]byte, 2)
	if _, err := io.ReadFull(c, h); err != nil {
		return nil, err
	}
	n := int(h[0])<<8 | int(h[1])
	if n < 12 || n > 64*1024 {
		return nil, fmt.Errorf("upstream frame %d out of range", n)
	}
	resp := make([]byte, n)
	if _, err := io.ReadFull(c, resp); err != nil {
		return nil, err
	}
	if idOf(raw) != idOf(resp) {
		return nil, fmt.Errorf("upstream transaction id mismatch")
	}
	return resp, nil
}

func idOf(b []byte) uint16 {
	if len(b) < 2 {
		return 0
	}
	return uint16(b[0])<<8 | uint16(b[1])
}

func hostOf(a net.Addr) string {
	h, _, err := net.SplitHostPort(a.String())
	if err != nil {
		return ""
	}
	return h
}

// sandboxOf maps a source IP to its sandbox name via the daemon-written
// snapshot (mtime-cached). Unknown sources (unattributed containers, the
// race before the first snapshot) fail closed.
func (f *fence) sandboxOf(ip string) string {
	if ip == "" {
		return ""
	}
	st := stampOf(IPsPath())
	f.ipMu.Lock()
	defer f.ipMu.Unlock()
	if f.ipMap == nil || !f.ipStat.fresh(st) {
		m := map[string]string{}
		if b, err := os.ReadFile(IPsPath()); err == nil {
			var v ipSnapshot
			if json.Unmarshal(b, &v) == nil && v.IPs != nil {
				m = v.IPs
			}
		}
		f.ipMap, f.ipStat = m, st
	}
	return f.ipMap[ip]
}

// allowed reports whether the qname matches the sandbox's allowlist.
// Entries are suffix matches (example.com covers api.example.com), lazily
// normalized (lowercased, trailing dot and leading "*." stripped). The
// config is re-read when its mtime changes, so `hotbedd egress … allowlist`
// edits apply live, no relay restart needed.
func (f *fence) allowed(sandbox, qname string) bool {
	cfg := f.config()
	if cfg == nil {
		return false
	}
	qname = strings.TrimSuffix(strings.ToLower(qname), ".")
	if qname == "" {
		return false
	}
	for _, e := range cfg.EgressAllowlist[sandbox] {
		e = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(e), "."))
		e = strings.TrimPrefix(e, "*.")
		if e == "" {
			continue
		}
		if e == qname || strings.HasSuffix(qname, "."+e) {
			return true
		}
	}
	return false
}

// config loads the config file, cached by mtime+size.
func (f *fence) config() *config.Config {
	st := stampOf(config.Path())
	f.cfgMu.Lock()
	defer f.cfgMu.Unlock()
	if f.cfg == nil || !f.cfgStat.fresh(st) {
		c, err := f.load()
		if err != nil {
			if f.cfg == nil { // first ever load failed: still not fatal
				return nil
			}
			return f.cfg // ride out a transient config parse error
		}
		f.cfg, f.cfgStat = c, st
	}
	return f.cfg
}

// log appends resolved-domain hits to the shared event file, deduped per
// (sandbox, ip) for this fence process's lifetime — the daemon's keeper
// installs new iptables ACCEPTs only for these lines.
func (f *fence) log(sandbox string, ips [][]byte) {
	if sandbox == "" || len(ips) == 0 {
		return
	}
	f.seenMu.Lock()
	defer f.seenMu.Unlock()
	var fresh []event
	for _, b := range ips {
		ip := net.IP(b).String()
		key := sandbox + "|" + ip
		if _, dup := f.seen[key]; dup {
			continue
		}
		f.seen[key] = struct{}{}
		fresh = append(fresh, event{Sandbox: sandbox, IP: ip})
	}
	if len(fresh) == 0 {
		return
	}
	w, err := os.OpenFile(EventsPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer w.Close()
	enc := json.NewEncoder(w)
	for _, e := range fresh {
		_ = enc.Encode(e)
	}
}
