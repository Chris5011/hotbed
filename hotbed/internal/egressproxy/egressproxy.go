// Package egressproxy is the enforcement point of the `strict` egress
// preset (issue #4): strict sandboxes have no network route out at all
// (an internal Docker network / an isolated libvirt network), so the only
// way to the internet is this HTTP(S) proxy outside the sandbox. It
// attributes every connection to its sandbox by source address, lets
// through only hosts on that sandbox's allowlist (CONNECT host / request
// host; no TLS interception) and records each requested host for the DNS
// tab. Nothing inside the sandbox — root included — can change any of it.
package egressproxy

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"hotbed/internal/dnsfence"
)

// Port is the proxy's listen port on the strict networks.
const Port = "3128"

// Handler serves CONNECT tunnels and absolute-URI plain HTTP requests.
type Handler struct {
	J    *dnsfence.Judge
	Dial func(network, addr string) (net.Conn, error) // test seam
}

// New builds a handler on the shared fence judge.
func New(j *dnsfence.Judge) *Handler {
	d := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	return &Handler{J: j, Dial: d.Dial}
}

// Serve listens on addr until the listener fails.
func Serve(addr string, h *Handler) error {
	srv := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 20 * time.Second}
	return srv.ListenAndServe()
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	src, _, _ := net.SplitHostPort(r.RemoteAddr)
	sb := h.J.Sandbox(src)
	if sb == "" || h.J.Mode(sb) != "strict" {
		// unknown source or not a strict sandbox: fail closed
		http.Error(w, "hotbed egress: this source is not a strict sandbox", http.StatusForbidden)
		return
	}
	target := r.Host
	if r.Method != http.MethodConnect {
		if r.URL == nil || !r.URL.IsAbs() {
			http.Error(w, "hotbed egress: forward proxy - absolute URLs or CONNECT only", http.StatusBadRequest)
			return
		}
		target = r.URL.Host
	}
	host, port := splitTarget(target, r.Method == http.MethodConnect)
	allowed := host != "" && h.J.Allowed(sb, host)
	h.J.Record(sb, host, !allowed)
	if !allowed {
		http.Error(w, fmt.Sprintf("hotbed egress: %s is not on %s's allowlist (allow it in the DNS tab)", host, sb),
			http.StatusForbidden)
		return
	}
	if r.Method == http.MethodConnect {
		h.tunnel(w, net.JoinHostPort(host, port))
		return
	}
	h.forward(w, r)
}

// tunnel connects and splices both directions (TLS stays end to end).
func (h *Handler) tunnel(w http.ResponseWriter, addr string) {
	up, err := h.Dial("tcp", addr)
	if err != nil {
		http.Error(w, "hotbed egress: "+err.Error(), http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		up.Close()
		http.Error(w, "hotbed egress: hijack unsupported", http.StatusInternalServerError)
		return
	}
	down, buf, err := hj.Hijack()
	if err != nil {
		up.Close()
		return
	}
	_, _ = down.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
	if n := buf.Reader.Buffered(); n > 0 { // bytes the client sent early
		b, _ := buf.Reader.Peek(n)
		_, _ = up.Write(b)
	}
	splice(down, up)
}

// forward relays one plain HTTP request (hop-by-hop headers dropped).
func (h *Handler) forward(w http.ResponseWriter, r *http.Request) {
	out := r.Clone(r.Context())
	out.RequestURI = ""
	for _, hh := range []string{"Proxy-Connection", "Proxy-Authorization", "Connection", "Keep-Alive", "Upgrade"} {
		out.Header.Del(hh)
	}
	tr := &http.Transport{Dial: h.Dial, Proxy: nil, ResponseHeaderTimeout: 60 * time.Second}
	resp, err := tr.RoundTrip(out)
	if err != nil {
		http.Error(w, "hotbed egress: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// splitTarget normalizes host[:port] (default 443 for CONNECT, 80 else).
func splitTarget(t string, connect bool) (string, string) {
	host, port, err := net.SplitHostPort(t)
	if err != nil {
		host, port = t, "80"
		if connect {
			port = "443"
		}
	}
	host = strings.TrimSuffix(strings.ToLower(strings.Trim(host, "[]")), ".")
	return host, port
}

func splice(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	cp := func(dst, src net.Conn) {
		defer wg.Done()
		_, _ = io.Copy(dst, src)
		if c, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = c.CloseWrite()
		}
	}
	go cp(a, b)
	go cp(b, a)
	wg.Wait()
	a.Close()
	b.Close()
}

// Forward is a plain TCP forwarder (the gateway container hands strict
// sandboxes the hotbed LLM proxy: listen -> target).
func Forward(listen, target string) error {
	l, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	for {
		c, err := l.Accept()
		if err != nil {
			return err
		}
		go func(c net.Conn) {
			up, err := net.DialTimeout("tcp", target, 10*time.Second)
			if err != nil {
				fmt.Fprintf(os.Stderr, "hotbed egress forward %s -> %s: %v\n", listen, target, err)
				c.Close()
				return
			}
			splice(c, up)
		}(c)
	}
}
