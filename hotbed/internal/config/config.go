// Package config manages hotbedd's global settings file:
// providers (upstream LLM endpoints incl. keys), default model, proxy
// knobs, pricing overrides. It can import providers from the host's
// existing ~/.config/opencode/opencode.json.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"hotbed/internal/audit"
	"hotbed/internal/env"
)

// Provider is one upstream LLM endpoint entry.
type Provider struct {
	BaseURL string   `json:"base_url"`
	APIKey  string   `json:"api_key,omitempty"`
	Models  []string `json:"models,omitempty"`
	Style   string   `json:"style,omitempty"` // openai|anthropic (default openai)
}

type Proxy struct {
	Port                string   `json:"port,omitempty"`
	DefaultBudgetUSD    *float64 `json:"default_budget_usd,omitempty"`    // nil = 0 = unlimited
	DefaultBudgetTokens *int     `json:"default_budget_tokens,omitempty"` // nil = 0 = unlimited
	Bind                string   `json:"bind,omitempty"`                  // default 0.0.0.0 (sandboxes must reach it)
	// DNSPort is the port of the DNS fence inside the docker engine netns
	// (the relay / main daemon listener DNAT'ed container :53 traffic to).
	// Empty = default. The fence answers egress-allowlist DNS.
	DNSPort string `json:"dns_port,omitempty"`
}

type OC struct {
	DefaultProvider string              `json:"default_provider,omitempty"`
	DefaultModel    string              `json:"default_model,omitempty"`
	Providers       map[string]Provider `json:"providers,omitempty"`
}

// Config mirrors the JSON file 1:1.
type Config struct {
	Opencode     OC                    `json:"opencode"`
	Proxy        Proxy                 `json:"proxy"`
	Prices       map[string][2]float64 `json:"prices,omitempty"`        // model or "prefix/" -> $/Mtok in,out
	Budgets      map[string]float64    `json:"budgets,omitempty"`       // sandbox -> USD wallet (0 = unlimited)
	BudgetTokens map[string]int        `json:"budget_tokens,omitempty"` // sandbox -> token wallet (0 = unlimited)
	// Pinned sandboxes keep their burn-ledger history visible and survive
	// the "purge stale records" bulk delete (dashboard Burn view).
	Pinned []string `json:"pinned_sandboxes,omitempty"`
	// Sweeper auto-stops idle kit sandboxes (P3 item E); nil = disabled.
	Sweeper *Sweeper `json:"sweeper,omitempty"`
	// EgressAllowlist maps sandbox name -> domains its egress-allowlist
	// fence may reach (everything else drops). Installed via
	// `hotbedd egress NAME allowlist DOMAINS...` (list only = show).
	EgressAllowlist map[string][]string `json:"egress_allowlist,omitempty"`
}

// Sweeper auto-stops idle kit sandboxes (containers with the dev.hotbed label,
// not VM-backed ones): a sandbox whose CPU stays near zero for
// IdleStopMinutes gets stopped (audit-logged; `hbd` start restarts it).
type Sweeper struct {
	IdleStopMinutes int `json:"idle_stop_minutes,omitempty"` // 0 = disabled
}

// IdleStopMinutes returns the configured threshold (0 = disabled).
func (c *Config) IdleStopMinutes() int {
	if c.Sweeper == nil {
		return 0
	}
	return c.Sweeper.IdleStopMinutes
}

// Path returns the config file location.
func Path() string { return filepath.Join(env.ConfigDir(), "config.json") }

// SetEgressAllowlist normalizes and persists a sandbox's egress-allowlist
// domains (suffix matches; leading "*." and trailing dots are stripped).
// Callers apply the fence themselves (engine.ApplyEgressFence) — this only
// owns the config side. Returns the normalized list.
func (c *Config) SetEgressAllowlist(name string, domains []string) []string {
	norm := make([]string, 0, len(domains))
	seen := map[string]bool{}
	for _, d := range domains {
		d = strings.ToLower(strings.TrimSpace(d))
		d = strings.TrimSuffix(d, ".")
		d = strings.TrimPrefix(d, "*.")
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		norm = append(norm, d)
	}
	if c.EgressAllowlist == nil {
		c.EgressAllowlist = map[string][]string{}
	}
	c.EgressAllowlist[name] = norm
	_ = c.Save()
	audit.Record("egress.allowlist", name, strings.Join(norm, ","))
	return norm
}

// EgressAllowlistFor returns the configured domains of a sandbox.
func (c *Config) EgressAllowlistFor(name string) []string {
	return c.EgressAllowlist[name]
}

// HostOpenCodePath is the host-side opencode config (import source).
func HostOpenCodePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "opencode", "opencode.json")
}

// Open loads the config; if absent it creates a headless default so the
// daemon still runs (proxy disabled until a provider exists).
func Open() (*Config, error) {
	p := Path()
	if _, err := os.Stat(p); err != nil {
		c := &Config{}
		if err := c.Save(); err != nil {
			return nil, err
		}
		return c, nil
	}
	return Load()
}

func Load() (*Config, error) {
	b, err := os.ReadFile(Path())
	if err != nil {
		return nil, err
	}
	c := &Config{}
	if err := json.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", Path(), err)
	}
	c.sane()
	return c, nil
}

func (c *Config) sane() {
	if c.Proxy.Port == "" {
		c.Proxy.Port = env.DefaultProxyPort
	}
	if c.Proxy.DNSPort == "" {
		c.Proxy.DNSPort = env.DefaultDNSPort
	}
	if c.Proxy.Bind == "" {
		c.Proxy.Bind = "0.0.0.0"
	}
	if c.EgressAllowlist == nil {
		c.EgressAllowlist = map[string][]string{}
	}
	if c.Proxy.DefaultBudgetUSD != nil && *c.Proxy.DefaultBudgetUSD < 0 {
		zero := 0.0
		c.Proxy.DefaultBudgetUSD = &zero
	}
	if c.Proxy.DefaultBudgetTokens != nil && *c.Proxy.DefaultBudgetTokens < 0 {
		zero := 0
		c.Proxy.DefaultBudgetTokens = &zero
	}
	if c.Budgets == nil {
		c.Budgets = map[string]float64{} // nil maps marshal as null — keep {}
	}
	if c.BudgetTokens == nil {
		c.BudgetTokens = map[string]int{}
	}
	dropNegativeUSD(c.Budgets)
	dropNegativeTok(c.BudgetTokens)
	if c.Sweeper != nil && c.Sweeper.IdleStopMinutes < 0 {
		c.Sweeper.IdleStopMinutes = 0
	}
}

func dropNegativeUSD(m map[string]float64) {
	for k, v := range m {
		if v < 0 {
			delete(m, k)
		}
	}
}

func dropNegativeTok(m map[string]int) {
	for k, v := range m {
		if v < 0 {
			delete(m, k)
		}
	}
}

// Save writes the file with 0700 dir/0600 file perms (it holds secrets).
func (c *Config) Save() error {
	c.sane()
	if err := os.MkdirAll(env.ConfigDir(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(Path(), b, 0o600)
}

// DefaultProvider returns the fallback provider id.
func (c *Config) DefaultProvider() string {
	if c.Opencode.DefaultProvider != "" && c.Has(c.Opencode.DefaultProvider) {
		return c.Opencode.DefaultProvider
	}
	for id := range c.Opencode.Providers {
		return id
	}
	return ""
}

// Has checks provider existence.
func (c *Config) Has(id string) bool {
	_, ok := c.Opencode.Providers[id]
	return ok
}

// Provider looks one up.
func (c *Config) Provider(id string) (Provider, bool) {
	p, ok := c.Opencode.Providers[id]
	return p, ok
}

// ProviderStyle normalizes the wire style.
func (p Provider) StyleWire() string {
	if p.Style == "anthropic" {
		return "anthropic"
	}
	return "openai"
}

// DefaultBudget returns the default budget ($/sandbox, 0 = unlimited).
func (c *Config) DefaultBudget() float64 {
	if c.Proxy.DefaultBudgetUSD == nil {
		return 0
	}
	return *c.Proxy.DefaultBudgetUSD
}

// DefaultTokenWallet returns the default token budget (0 = unlimited).
func (c *Config) DefaultTokenWallet() int {
	if c.Proxy.DefaultBudgetTokens == nil {
		return 0
	}
	return *c.Proxy.DefaultBudgetTokens
}

// USDWallet resolves a sandbox's USD wallet (explicit entry beats the
// default; 0 = unlimited). Missing sandbox = default.
func (c *Config) USDWallet(sandbox string) float64 {
	if v, ok := c.Budgets[sandbox]; ok {
		return v
	}
	return c.DefaultBudget()
}

// TokenWallet resolves a sandbox's token wallet (explicit entry beats the
// default; 0 = unlimited). Missing sandbox = default.
func (c *Config) TokenWallet(sandbox string) int {
	if v, ok := c.BudgetTokens[sandbox]; ok {
		return v
	}
	return c.DefaultTokenWallet()
}

// EachWallet visits every sandbox that holds an explicit wallet (USD or
// token), once, in stable name order. Used to seed burn-view rows for
// sandboxes that have wallets but no proxy calls yet.
func (c *Config) EachWallet(fn func(name string)) {
	names := map[string]bool{}
	for k := range c.Budgets {
		names[k] = true
	}
	for k := range c.BudgetTokens {
		names[k] = true
	}
	ordered := make([]string, 0, len(names))
	for k := range names {
		ordered = append(ordered, k)
	}
	sort.Strings(ordered)
	for _, k := range ordered {
		fn(k)
	}
}

// SetWallets updates one or both wallets of a sandbox and saves. A nil
// pointer leaves that wallet unchanged; 0 = unlimited. Both nil = no-op.
func (c *Config) SetWallets(sandbox string, usd *float64, tokens *int) error {
	if usd == nil && tokens == nil {
		return nil
	}
	c.sane()
	if usd != nil {
		if c.Budgets == nil {
			c.Budgets = map[string]float64{}
		}
		c.Budgets[sandbox] = *usd
	}
	if tokens != nil {
		if c.BudgetTokens == nil {
			c.BudgetTokens = map[string]int{}
		}
		c.BudgetTokens[sandbox] = *tokens
	}
	c.sane()
	if err := c.Save(); err != nil {
		return err
	}
	audit.Record("wallet.set", sandbox, walletDetail(usd, tokens))
	return nil
}

func walletDetail(usd *float64, tokens *int) string {
	parts := []string{}
	if usd != nil {
		parts = append(parts, fmt.Sprintf("usd=%.6g", *usd))
	}
	if tokens != nil {
		parts = append(parts, fmt.Sprintf("tokens=%d", *tokens))
	}
	return strings.Join(parts, " ")
}

// IsPinned reports whether the burn ledger history of this sandbox name is
// pinned (pinned names stay visible in the dashboard and are exempt from
// the "purge stale records" bulk delete).
func (c *Config) IsPinned(name string) bool {
	if name == "" {
		return false
	}
	for _, p := range c.Pinned {
		if p == name {
			return true
		}
	}
	return false
}

// SetPinned adds or removes a burn-ledger pin and saves. Reports the final
// state so callers can echo it back to the dashboard.
func (c *Config) SetPinned(name string, on bool) (bool, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return false, fmt.Errorf("name required")
	}
	had := c.IsPinned(name)
	if on == had {
		return had, nil // idempotent — no file churn
	}
	if on {
		c.Pinned = append(c.Pinned, name)
	} else {
		var kept []string
		for _, p := range c.Pinned {
			if p != name {
				kept = append(kept, p)
			}
		}
		c.Pinned = kept
	}
	if err := c.Save(); err != nil {
		return on, err
	}
	if on {
		audit.Record("ledger.pin", name, "")
	} else {
		audit.Record("ledger.unpin", name, "")
	}
	return on, nil
}

// RemoveWallets drops a sandbox's USD/token wallet entries and saves
// (sandbox rm lifecycle: a recreated sandbox starts wallet-less — the user
// re-arms budgets deliberately). Returns whether anything was removed.
func (c *Config) RemoveWallets(sandbox string) (bool, error) {
	removed := false
	if _, ok := c.Budgets[sandbox]; ok {
		delete(c.Budgets, sandbox)
		removed = true
	}
	if _, ok := c.BudgetTokens[sandbox]; ok {
		delete(c.BudgetTokens, sandbox)
		removed = true
	}
	if !removed {
		return false, nil
	}
	c.sane()
	if err := c.Save(); err != nil {
		return removed, err
	}
	audit.Record("wallet.clear", sandbox, "wallets removed with the sandbox")
	return removed, nil
}

// Mask replaces secret values with the sentinel (for GET /api/config).
const Masked = "<masked>"

// MaskedCopy returns a deep copy with api_key fields masked.
func (c *Config) MaskedCopy() *Config {
	b, _ := json.Marshal(c)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	maskWalk(out)
	b2, _ := json.Marshal(out)
	var cc *Config
	_ = json.Unmarshal(b2, &cc)
	return cc
}

func maskWalk(m any) {
	switch v := m.(type) {
	case map[string]any:
		for k, vv := range v {
			if (k == "api_key" || k == "apiKey") && strings.TrimSpace(asString(vv)) != "" {
				v[k] = Masked
			} else {
				maskWalk(vv)
			}
		}
	case []any:
		for _, e := range v {
			maskWalk(e)
		}
	}
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

// MergeFrom overwrites c with incoming fields, importing keys but keeping
// existing values where the incoming field equals the mask sentinel.
func (c *Config) MergeFrom(in *Config) {
	// providers
	if in.Opencode.Providers != nil {
		if c.Opencode.Providers == nil {
			c.Opencode.Providers = map[string]Provider{}
		}
		for id, np := range in.Opencode.Providers {
			if np.APIKey == Masked || np.APIKey == "" {
				if old, ok := c.Opencode.Providers[id]; ok {
					np.APIKey = old.APIKey
				} else {
					np.APIKey = ""
				}
			}
			c.Opencode.Providers[id] = np
		}
	}
	if in.Opencode.DefaultProvider != "" {
		c.Opencode.DefaultProvider = in.Opencode.DefaultProvider
	}
	if in.Opencode.DefaultModel != "" {
		c.Opencode.DefaultModel = in.Opencode.DefaultModel
	}
	if in.Proxy.Port != "" {
		c.Proxy.Port = in.Proxy.Port
	}
	if in.Proxy.Bind != "" {
		c.Proxy.Bind = in.Proxy.Bind
	}
	if in.Proxy.DefaultBudgetUSD != nil && *in.Proxy.DefaultBudgetUSD >= 0 {
		c.Proxy.DefaultBudgetUSD = in.Proxy.DefaultBudgetUSD
	}
	if in.Proxy.DefaultBudgetTokens != nil && *in.Proxy.DefaultBudgetTokens >= 0 {
		c.Proxy.DefaultBudgetTokens = in.Proxy.DefaultBudgetTokens
	}
	if in.Budgets != nil {
		if c.Budgets == nil {
			c.Budgets = map[string]float64{}
		}
		for k, v := range in.Budgets {
			c.Budgets[k] = v
		}
	}
	if in.BudgetTokens != nil {
		if c.BudgetTokens == nil {
			c.BudgetTokens = map[string]int{}
		}
		for k, v := range in.BudgetTokens {
			c.BudgetTokens[k] = v
		}
	}
	if in.Prices != nil {
		if c.Prices == nil {
			c.Prices = map[string][2]float64{}
		}
		for k, v := range in.Prices {
			c.Prices[k] = v
		}
	}
}

// ImportHostOpenCode pulls provider entries from ~/.config/opencode/opencode.json
// (base_url, api_key, model list) into the hotbedd config. Returns count imported.
func (c *Config) ImportHostOpenCode() (int, error) {
	b, err := os.ReadFile(HostOpenCodePath())
	if err != nil {
		return 0, fmt.Errorf("read host opencode config: %w", err)
	}
	var src struct {
		Provider map[string]json.RawMessage `json:"provider"`
		Model    string                     `json:"model"`
	}
	if err := json.Unmarshal(b, &src); err != nil {
		return 0, fmt.Errorf("parse host opencode config: %w", err)
	}
	n := 0
	if c.Opencode.Providers == nil {
		c.Opencode.Providers = map[string]Provider{}
	}
	for id, raw := range src.Provider {
		p := c.Opencode.Providers[id]
		var h struct {
			NPM     string `json:"npm"`
			Name    string `json:"name"`
			Options struct {
				BaseURL string `json:"baseURL"`
				APIKey  string `json:"apiKey"`
			} `json:"options"`
			Models map[string]json.RawMessage `json:"models"`
		}
		if err := json.Unmarshal(raw, &h); err != nil {
			continue
		}
		p.BaseURL = h.Options.BaseURL
		if h.Options.APIKey != "" {
			p.APIKey = h.Options.APIKey
		}
		for mid := range h.Models {
			p.Models = append(p.Models, mid)
		}
		p.Models = dedupe(p.Models)
		if strings.HasPrefix(h.NPM, "@ai-sdk/anthropic") || id == "anthropic" {
			p.Style = "anthropic"
		}
		c.Opencode.Providers[id] = p
		n++
	}
	if c.Opencode.DefaultModel == "" && src.Model != "" && strings.Contains(src.Model, "/") {
		parts := strings.SplitN(src.Model, "/", 2)
		if c.Opencode.DefaultProvider == "" {
			c.Opencode.DefaultProvider = parts[0]
		}
		c.Opencode.DefaultModel = parts[1]
	}
	return n, nil
}

// MigrateFromKeys copies legacy per-sandbox budgets out of keys.json into
// config.json (P2 stored wallets there), then rewrites keys.json without
// the "budgets" map so the move is one-time. Idempotent: an absent keys
// file or one without budgets is a no-op; explicit config entries win on
// conflict. Called once at every server/CLI store bootstrap; returns the
// number of sandboxes migrated.
func MigrateFromKeys() (int, error) {
	p := filepath.Join(env.DataDir(), "keys.json")
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	var kf struct {
		Keys    json.RawMessage    `json:"keys"`
		Budgets map[string]float64 `json:"budgets"`
	}
	if err := json.Unmarshal(b, &kf); err != nil {
		return 0, fmt.Errorf("parse %s: %w", p, err)
	}
	if len(kf.Budgets) == 0 {
		return 0, nil
	}
	c, err := Open()
	if err != nil {
		return 0, err
	}
	if c.Budgets == nil {
		c.Budgets = map[string]float64{}
	}
	n := 0
	for k, v := range kf.Budgets {
		if _, ok := c.Budgets[k]; !ok {
			c.Budgets[k] = v
			n++
		}
	}
	if err := c.Save(); err != nil {
		return n, err
	}
	// rewrite keys.json without budgets (keys round-trip verbatim)
	out := struct {
		Keys json.RawMessage `json:"keys"`
	}{Keys: kf.Keys}
	b2, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return n, err
	}
	return n, os.WriteFile(p, b2, 0o600)
}

func dedupe(in []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, s := range in {
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
