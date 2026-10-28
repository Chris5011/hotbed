// Package proxy is hotbedd's token-accounting LLM proxy.
//
// A call path looks like:
//
//	sandbox (opencode TUI) ─ Authorization: Bearer hbk-<sandbox>-<rand>
//	    ──▶ hotbedd proxy :7775 ── rewrites auth, resolves upstream provider
//	    ──▶ real endpoint. Streams pass through verbatim; usage is parsed
//	    from SSE frames (OpenAI final chunk / Anthropic messages) or
//	    estimated as bytes/4, then written to the ledger keyed by sandbox.
//
// Wallets (USD and tokens, from config.json) are enforced BEFORE
// forwarding (HTTP 429 when a wallet is dry).
// Unlike the dashboard this listener binds 0.0.0.0 so containers/VMs can
// reach it; every request needs a valid minted key or it is rejected.
package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"hotbed/internal/config"
	"hotbed/internal/keys"
	"hotbed/internal/ledger"
)

// authBackoff rate-limits failed authentication per remote IP. The proxy
// listens on 0.0.0.0 by design; after 10 failures inside a minute the same
// IP pays a capped sleep before each further attempt (unknown-key guessing,
// including timing probes, gets expensive). Successful auth clears the
// counter. In-memory only — a daemon restart resets it (fine: every sandbox
// restarts too).
type authBackoff struct {
	mu    sync.Mutex
	fails map[string]*failRec
}

type failRec struct {
	n     int
	since time.Time
}

const (
	backoffThreshold = 10
	backoffWindow    = time.Minute
	backoffMaxSleep  = 2 * time.Second
)

func newAuthBackoff() *authBackoff { return &authBackoff{fails: map[string]*failRec{}} }

func remoteHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || host == "" {
		return "local"
	}
	return host
}

func (b *authBackoff) before(r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ip := remoteHost(r)
	rec := b.fails[ip]
	if rec != nil && time.Since(rec.since) > backoffWindow {
		rec = nil
	}
	if rec == nil || rec.n < backoffThreshold {
		return
	}
	sleep := min(backoffMaxSleep, time.Duration(rec.n)*100*time.Millisecond)
	if sleep > 0 {
		b.mu.Unlock()
		time.Sleep(sleep)
		b.mu.Lock()
	}
}

func (b *authBackoff) onFail(r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ip := remoteHost(r)
	rec := b.fails[ip]
	if rec != nil && time.Since(rec.since) > backoffWindow {
		rec = nil
	}
	if rec == nil {
		rec = &failRec{since: time.Now()}
		b.fails[ip] = rec
	}
	rec.n++
}

func (b *authBackoff) onOK(r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.fails, remoteHost(r))
}

// Service bundles the deps for the proxy handler.
type Service struct {
	Cfg  *config.Config
	Keys *keys.Store
	Led  *ledger.Ledger
	HTTP *http.Client
	bo   *authBackoff
}

// New builds a service. The upstream client only bounds the TIME UNTIL
// response HEADERS (30 s): a full client.Timeout would kill SSE streams
// mid-generation, but no bound at all lets a wedged upstream hold forked
// request goroutines open forever (fd exhaustion).
func New(cfg *config.Config, ks *keys.Store, led *ledger.Ledger) *Service {
	return &Service{Cfg: cfg, Keys: ks, Led: led, bo: newAuthBackoff(),
		HTTP: &http.Client{Transport: &http.Transport{
			ResponseHeaderTimeout: 30 * time.Second,
		}}}
}

// Port resolves the configured proxy port.
func (p *Service) Port() string { return p.Cfg.Proxy.Port }

// Addr binds 0.0.0.0:<port> by default.
func (p *Service) Addr() string {
	return p.Cfg.Proxy.Bind + ":" + p.Port()
}

type errBody struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	var e errBody
	e.Error.Message = msg
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(e)
}

func (p *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	p.bo.before(r) // unknown-key guessing pays an escalating sleep after a burst

	// stateless config view: CLI edits apply without a daemon restart
	cfg, err := config.Load()
	if err != nil {
		writeErr(w, 500, "hotbedd proxy: config unreadable: "+err.Error())
		return
	}
	p.Cfg = cfg
	p.Led = ledger.Open(cfg.Prices) // pricing is stateless too

	tok := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if tok == "" {
		tok = strings.TrimSpace(r.Header.Get("x-hotbed-key"))
	}
	if tok == "" {
		writeErr(w, 401, "hotbedd proxy: missing per-sandbox key (Authorization: Bearer hbk-…); mint one with `hotbedd key mint SANDBOX`")
		return
	}
	k, ok := p.Keys.ByToken(tok)
	if !ok {
		p.bo.onFail(r)
		writeErr(w, 401, "hotbedd proxy: unknown or revoked key")
		return
	}
	p.bo.onOK(r)
	provID := k.Provider
	if provID == "" {
		provID = p.Cfg.DefaultProvider()
	}
	prov, ok := p.Cfg.Provider(provID)
	if !ok || strings.TrimSpace(prov.BaseURL) == "" {
		writeErr(w, 500, "hotbedd proxy: provider "+provID+" has no upstream base_url in config — run `hotbedd config init`")
		return
	}

	// wallet checks BEFORE spending anything (USD and token wallets)
	usdBudget := cfg.USDWallet(k.Sandbox)
	tokBudget := cfg.TokenWallet(k.Sandbox)
	if usdBudget > 0 || tokBudget > 0 {
		s := p.Led.Summarize(k.Sandbox)
		if usdBudget > 0 && s.Today >= usdBudget {
			writeErr(w, 429, fmt.Sprintf("hotbedd proxy: USD wallet exhausted for sandbox %s (today $%.6g ≥ $%.6g) — raise with `hotbedd budget %s <usd>`", k.Sandbox, s.Today, usdBudget, k.Sandbox))
			return
		}
		if tokBudget > 0 && s.TodayTokensIn+s.TodayTokensOut >= tokBudget {
			writeErr(w, 429, fmt.Sprintf("hotbedd proxy: token wallet exhausted for sandbox %s (today %d ≥ %d tokens) — raise with `hotbedd budget %s --tokens N`", k.Sandbox, s.TodayTokensIn+s.TodayTokensOut, tokBudget, k.Sandbox))
			return
		}
	}

	// not found upstream is fine to surface 1:1 below
	if r.URL.Path == "/" || r.URL.Path == "" {
		writeErr(w, 404, "hotbedd proxy: empty path")
		return
	}

	// read body once (we need the model for pricing). Bodies up to 8 MiB are
	// buffered; bigger ones MUST NOT be truncated - a truncated JSON body
	// turns into an opaque upstream "Invalid request payload". For oversized
	// requests we stream the original body through unmodified and skip model
	// parsing in the ledger (the est is then partial; documented behaviour).
	// The reader caps at maxBuffered+1 on purpose: reading more than cap
	// alone proves overflow, and (unlike http.MaxBytesReader) a LimitReader
	// never consumes bytes from the original body beyond the cap it reports.
	const maxBuffered = 8 << 20
	body, _ := io.ReadAll(io.LimitReader(r.Body, maxBuffered+1))
	overflow := int64(len(body)) == maxBuffered+1
	model := ""
	var reqMeta struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &reqMeta)
	model = reqMeta.Model

	dst := strings.TrimRight(prov.BaseURL, "/") + "/" + strings.TrimPrefix(r.URL.Path, "/v1/")
	dst = strings.TrimSuffix(dst, "/")
	if r.URL.RawQuery != "" {
		dst += "?" + r.URL.RawQuery
	}

	// opt-in request rewrites for upstreams that need them (compat.go)
	if !overflow && prov.StyleWire() == "anthropic" && prov.HasCompat(compatAnthropicGateway) {
		if nb, ok := rewriteAnthropicGateway(body); ok {
			body = nb
		}
	}

	var up *http.Request
	if overflow {
		// replay the full original body: everything read so far, then the rest
		up, err = http.NewRequest(r.Method, dst,
			io.MultiReader(bytes.NewReader(body), r.Body))
		if err == nil && r.ContentLength > 0 {
			up.ContentLength = r.ContentLength
		}
	} else {
		up, err = http.NewRequest(r.Method, dst, bytes.NewReader(body))
	}
	if err != nil {
		writeErr(w, 502, "hotbedd proxy: "+err.Error())
		return
	}
	// forward banal headers, replace auth
	for _, h := range []string{"Content-Type", "Accept", "User-Agent", "X-Request-Id"} {
		if v := r.Header.Get(h); v != "" {
			up.Header.Set(h, v)
		}
	}
	if prov.StyleWire() == "anthropic" {
		up.Header.Set("x-api-key", prov.APIKey)
		up.Header.Set("anthropic-version", "2023-06-01")
	} else {
		up.Header.Set("Authorization", "Bearer "+prov.APIKey)
	}

	resp, err := p.HTTP.Do(up)
	if err != nil {
		writeErr(w, 502, "hotbedd proxy: upstream call failed: "+err.Error())
		return
	}
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	// copy response headers minus hop-by-hop
	for hk, hv := range resp.Header {
		switch strings.ToLower(hk) {
		case "transfer-encoding", "connection", "content-length", "server", "date", "content-encoding":
			continue
		}
		for _, v := range hv {
			w.Header().Add(hk, v)
		}
	}

	if ct != "" && strings.Contains(ct, "text/event-stream") {
		w.Header().Set("Content-Type", ct)
		w.WriteHeader(resp.StatusCode)
		in, out, est := p.streamCopy(w, resp.Body, ledger.Estimate(string(body)))
		_, _ = p.Led.Record(ledger.Call{
			Sandbox: k.Sandbox, Provider: provID, Agent: k.Agent, Model: model,
			In: in, Out: out, Est: est,
			DurMS: time.Since(start).Milliseconds(), Status: resp.StatusCode, Path: r.URL.Path,
		})
		return
	}

	// non-stream: buffer, count usage, answer verbatim
	raw, _ := io.ReadAll(resp.Body)
	w.Header().Set("Content-Length", fmt.Sprint(len(raw)))
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(raw)

	in, out, known := parseUsageBody(raw)
	estFlag := !known
	if !known {
		in = ledger.Estimate(string(body))
		out = 0
	}
	_, _ = p.Led.Record(ledger.Call{
		Sandbox: k.Sandbox, Provider: provID, Agent: k.Agent, Model: model,
		In: in, Out: out, Est: estFlag,
		DurMS: time.Since(start).Milliseconds(), Status: resp.StatusCode, Path: r.URL.Path,
	})
}

func (p *Service) streamCopy(w http.ResponseWriter, body io.Reader, inEst int) (inTok, outTok int, est bool) {
	br := bufio.NewReaderSize(body, 64<<10)
	fl, _ := w.(http.Flusher)
	var frame strings.Builder // SSE data payloads until a stream terminator
	var text strings.Builder  // extracted delta text (fallback estimate)
	known := false

	flushFrame := func() {
		if i, o, ok := parseUsageFrames(frame.String()); ok {
			inTok, outTok, known = i, o, true
		} else if !known {
			text.WriteString(extractDeltaText(frame.String()))
		}
		frame.Reset()
	}

	for {
		rawLine, err := br.ReadBytes('\n')
		if len(rawLine) > 0 {
			_, _ = w.Write(rawLine)
			if fl != nil {
				fl.Flush()
			}
			line := strings.TrimRight(string(rawLine), "\r\n")
			if strings.HasPrefix(line, "data:") {
				payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				frame.WriteString(payload)
				frame.WriteString("\n")
				// stream terminators: OpenAI/chat ([DONE]) or Anthropic message_stop
				if payload == "[DONE]" || strings.Contains(payload, "message_stop") {
					flushFrame()
				}
			}
		}
		if err != nil {
			break
		}
	}
	// stream may end without a terminator line (disconnects): parse what we have
	if frame.Len() > 0 {
		flushFrame()
	}
	if known {
		return inTok, outTok, false
	}
	return inEst, ledger.Estimate(text.String()), true
}

// parseUsageFrames extracts (in,out) from buffered SSE data payloads,
// understanding OpenAI final-chunk usage and Anthropic message_start/_delta.
func parseUsageFrames(frames string) (int, int, bool) {
	in, out, ok := 0, 0, false
	for _, ln := range strings.Split(frames, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || ln == "[DONE]" {
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(ln), &m) != nil {
			continue
		}
		if u, okk := m["usage"].(map[string]any); okk {
			if v := num(u, "prompt_tokens"); v > 0 {
				in = v
			}
			if v := num(u, "input_tokens"); v > 0 {
				in = v
			}
			if v := num(u, "completion_tokens"); v > 0 {
				out = v
			}
			if v := num(u, "output_tokens"); v > 0 {
				out = v
			}
			if in > 0 || out > 0 {
				ok = true
			}
		}
		if rsp, okr := m["response"].(map[string]any); okr { // openai responses: response.completed
			if u, oku := rsp["usage"].(map[string]any); oku {
				if v := num(u, "input_tokens"); v > 0 {
					in = v
					ok = true
				}
				if v := num(u, "output_tokens"); v > 0 {
					out = v
					ok = true
				}
			}
		}
		if msg, okm := m["message"].(map[string]any); okm { // anthropic message_start
			if u, oku := msg["usage"].(map[string]any); oku {
				if v := num(u, "input_tokens"); v > 0 {
					in = v
					ok = true
				}
			}
		}
		if t := m["type"]; t == "message_delta" {
			if u, oku := m["usage"].(map[string]any); oku {
				if v := num(u, "output_tokens"); v > 0 {
					out = v
					ok = true
				}
			}
		}
	}
	return in, out, ok
}

func num(m map[string]any, key string) int {
	if v, ok := m[key].(float64); ok {
		return int(v)
	}
	return 0
}

func extractDeltaText(frameJSON string) string {
	var m struct {
		Choices []struct {
			Delta struct {
				Content   string `json:"content"`
				Reasoning string `json:"reasoning_content"`
			} `json:"delta"`
		} `json:"choices"`
		// anthropic: {"text"|"content": …}; openai responses: a plain string
		Delta json.RawMessage `json:"delta"`
	}
	if json.Unmarshal([]byte(frameJSON), &m) != nil {
		return ""
	}
	var b strings.Builder
	for _, c := range m.Choices {
		b.WriteString(c.Delta.Content)
		b.WriteString(c.Delta.Reasoning)
	}
	var s string
	var obj struct {
		Text    string `json:"text"`
		Content string `json:"content"`
	}
	if json.Unmarshal(m.Delta, &s) == nil {
		b.WriteString(s)
	} else if json.Unmarshal(m.Delta, &obj) == nil {
		b.WriteString(obj.Text)
		b.WriteString(obj.Content)
	}
	return b.String()
}

// parseUsageBody handles non-stream JSON responses.
func parseUsageBody(raw []byte) (int, int, bool) {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return 0, 0, false
	}
	if u, ok := m["usage"].(map[string]any); ok {
		in := num(u, "prompt_tokens")
		if in == 0 {
			in = num(u, "input_tokens")
		}
		out := num(u, "completion_tokens")
		if out == 0 {
			out = num(u, "output_tokens")
		}
		if in > 0 || out > 0 {
			return in, out, true
		}
	}
	return 0, 0, false
}
