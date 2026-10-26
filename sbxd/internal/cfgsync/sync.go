// Package cfgsync pushes the "global OpenCode settings" into sandboxes.
//
// Each synced sandbox gets its own opencode config pointing at the sbxd
// proxy (provider "sbx-proxy", apiKey = the sandbox's minted key). The
// upstream provider key never travels with it. Containers use a shifted
// XDG_CONFIG_HOME (the kit bind-mounts the real ~/.config/opencode into
// every container, so one shared file could not hold per-sandbox keys);
// VMs get /home/dev/.config/opencode/opencode.json replaced.
package cfgsync

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"sbxd/internal/config"
	"sbxd/internal/env"
	"sbxd/internal/keys"
	"sbxd/internal/sandbox"
)

// Transport writes a file inside a sandbox (implemented by attach).
type Transport interface {
	WriteRemote(path string, payload []byte) error
}

// Target is what a sandbox needs to talk to the proxy.
type Target struct {
	BaseURL     string `json:"base_url"`
	Provider    string `json:"provider"`
	Key         string `json:"key"`
	KeyMasked   string `json:"key_masked"`
	ConfigPath  string `json:"config_path"`
	XDGOverride bool   `json:"xdg_override"`
	Via         string `json:"via,omitempty"` // how the sandbox reaches the proxy
	Note        string `json:"note,omitempty"`
}

func storeDir() string { return filepath.Join(env.DataDir(), "synced") }

// Lookup returns a synced target, if any.
func Lookup(sandboxName string) (Target, bool) {
	b, err := os.ReadFile(filepath.Join(storeDir(), env.SafeName(sandboxName)+".json"))
	if err != nil {
		return Target{}, false
	}
	var t Target
	if json.Unmarshal(b, &t) != nil {
		return Target{}, false
	}
	return t, true
}

// LastSync reports when the sandbox last received a synced proxy target
// (mtime of its store entry). attach-session env stamps compare against
// this to know when a running shell pane is stale.
func LastSync(sandboxName string) (time.Time, bool) {
	fi, err := os.Stat(filepath.Join(storeDir(), env.SafeName(sandboxName)+".json"))
	if err != nil || fi.IsDir() {
		return time.Time{}, false
	}
	return fi.ModTime(), true
}

// LookupMasked returns only masked info (API responses).
func LookupMasked(sandboxName string) (Target, bool) {
	t, ok := Lookup(sandboxName)
	if ok {
		t.Key = ""
	}
	return t, ok
}

// RemoveTarget deletes a sandbox's stored sync target (rm lifecycle;
// missing file is already the desired end state, so that's not an error).
func RemoveTarget(sandboxName string) error {
	err := os.Remove(filepath.Join(storeDir(), env.SafeName(sandboxName)+".json"))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// SyncedNames lists sandboxes with a stored target (alphabetical).
func SyncedNames() []string {
	entries, err := os.ReadDir(storeDir())
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".json")
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// hostIP picks the WSL eth0 (public-to-sandboxes) address, best effort.
func HostIP() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	var fallback string
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.To4() == nil || !ipn.IP.IsPrivate() {
				continue
			}
			if ifc.Name == "eth0" {
				return ipn.IP.String()
			}
			fallback = ipn.IP.String()
		}
	}
	return fallback
}

// BaseURLFor is the best static guess per kind (dashboard fallback display):
// containers → WSL eth0 host IP; libvirt guests → NAT bridge IP; other VMs →
// guest loopback, which the sync flow backs with a persistent ssh tunnel.
func BaseURLFor(kind, backend, port string) string {
	var host string
	switch {
	case kind == sandbox.KindVM && backend == sandbox.BackendLibvirt:
		host = "192.168.122.1"
	case kind == sandbox.KindVM:
		host = "127.0.0.1" // via ssh -R tunnel (see attach.ResolveProxyURL)
	default:
		host = HostIP()
	}
	if host == "" {
		host = "127.0.0.1"
	}
	return "http://" + host + ":" + port + "/v1"
}

// cnConfigPath / vmConfigPath mirror the kit's home layouts.
const (
	cnConfigPath = "/home/sbx/.config/opencode-sbx/opencode/opencode.json"
	vmConfigPath = "/home/dev/.config/opencode/opencode.json"
)

// Apply writes the sandbox opencode config (proxy provider only) and lingers
// the target for attach-time env injection. provider=="" uses the default.
// baseURL is the tested-reachable proxy endpoint (see attach.ResolveProxyURL);
// via documents how the sandbox reaches it (tunnel / probed route).
func Apply(sb sandbox.Sandbox, cfg *config.Config, ks *keys.Store,
	t Transport, baseURL, via, provider string) (Target, error) {

	if len(cfg.Opencode.Providers) == 0 {
		return Target{}, fmt.Errorf("no providers in config — run `sbxd config init`")
	}
	if provider == "" {
		provider = cfg.DefaultProvider()
	}
	if provider == "" {
		return Target{}, fmt.Errorf("no provider available (config has no providers)")
	}
	prov, ok := cfg.Provider(provider)
	if !ok || strings.TrimSpace(prov.BaseURL) == "" {
		return Target{}, fmt.Errorf("provider %q lacks base_url in sbxd config", provider)
	}
	key, _, err := ks.EnsureKey(sb.Name, provider)
	if err != nil {
		return Target{}, err
	}

	base := baseURL
	if base == "" {
		base = BaseURLFor(sb.Kind, sb.Backend, cfg.Proxy.Port)
	}
	tgt := Target{
		BaseURL:   base,
		Via:       via,
		Provider:  provider,
		Key:       key.Token,
		KeyMasked: mask(key.Token),
	}
	models := prov.Models
	model := ""
	if cfg.Opencode.DefaultModel != "" {
		model = cfg.Opencode.DefaultModel
	} else if len(models) > 0 {
		model = models[0]
	}
	entry := map[string]any{
		"npm":  "@ai-sdk/openai-compatible",
		"name": "SBX proxy (" + provider + ")",
		"options": map[string]string{
			"baseURL": base,
			"apiKey":  key.Token,
		},
		"models": map[string]any{},
	}
	for _, m := range models {
		entry["models"].(map[string]any)[m] = map[string]string{"name": m}
	}
	doc := map[string]any{
		"$schema":  "https://opencode.ai/config.json",
		"provider": map[string]any{"sbx-proxy": entry},
	}
	if model != "" {
		doc["model"] = "sbx-proxy/" + model
	}

	var path string
	var payload []byte
	if sb.Kind == sandbox.KindContainer {
		tgt.XDGOverride = true
		path = cnConfigPath
		tgt.Note = "container: XDG_CONFIG_HOME shifted to keep per-sandbox keys out of the host-mounted config"
	} else {
		path = vmConfigPath
		tgt.Note = "VM: guest opencode config replaced by proxy-only provider (upstream key stays in sbxd)"
	}
	if tgt.Via != "" {
		tgt.Note += " — reach: " + tgt.Via
	}
	payload, err = json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return Target{}, err
	}
	if t == nil {
		return Target{}, fmt.Errorf("no write transport for %s", sb.Name)
	}
	if err := t.WriteRemote(path, payload); err != nil {
		return Target{}, err
	}
	tgt.ConfigPath = path

	if err := os.MkdirAll(storeDir(), 0o700); err != nil {
		return Target{}, err
	}
	b, _ := json.MarshalIndent(tgt, "", "  ")
	if err := os.WriteFile(filepath.Join(storeDir(), env.SafeName(sb.Name)+".json"), b, 0o600); err != nil {
		return Target{}, err
	}
	return tgt, nil
}

func mask(tok string) string {
	i := strings.LastIndex(tok, "-")
	if i < 0 || len(tok)-i < 6 {
		return "sbxk-••••"
	}
	return tok[:i] + "-" + tok[len(tok)-4:]
}
