// Package recipe reads declarative sandbox recipes (hbd.yaml) with a
// minimal YAML subset — pure stdlib, no YAML dependency.
//
// Supported shape (block style only):
//
//	# comment
//	name: agent-core
//	stack: go
//	ports: [2227:22]              # flow list or "- item" list
//	mounts:
//	  - ~/projects/core:/workspace/core
//	  - /tmp/cache:/cache:ro
//	budget: 5                     # USD wallet (0 = unlimited)
//	budget_tokens: 500000         # token wallet (0 = unlimited)
//	egress: proxy-only            # allow | proxy-only | none | allowlist
//	egress_allowlist: [api.openai.com, opencode.ai]   # allowlist domains (optional)
//	dns_track: true               # record DNS lookups (allow / allowlist only)
//	sync: true                    # push proxy-backed opencode config after spawn
//	provider: openwebui
//	model: openwebui/glm-4.7
//	env:
//	  FOO: bar                    # flat map "KEY: value"
//	attach: --oc                  # extra bin/hbd attach flag
//
// Rules: spaces (no tabs) for indents, "key: value" or "key:" followed by
// an indented block, sequences with "- " (scalars only), flow lists
// "[a, b]", values raw and trimmed (no quotes/anchors/multiline).
// Any construct outside the subset errors loudly instead of guessing.
package recipe

import (
	"fmt"
	"strconv"
	"strings"
)

// Recipe is the parsed hbd.yaml.
type Recipe struct {
	Name         string
	Stack        string
	Ports        []string
	Mounts       []string
	Env          map[string]string
	Budget       *float64 // nil = leave wallet untouched
	BudgetTokens *int     // nil = leave wallet untouched
	Egress       string   // "", "allow", "proxy-only", "none", "allowlist"
	EgressAllow  []string // allowlist domains (nil = keep the config's list)
	DNSTrack     bool     // record DNS lookups via the fence
	Sync         bool
	Provider     string
	Model        string
	Attach       string // extra bin/hbd attach flag, e.g. --oc
}

// --- linter nodes ----------------------------------------------------------

type node struct {
	kind   int // kindScalar | kindList | kindMap
	scalar string
	list   []node
	keys   []string
	m      map[string]node
}

const (
	kindScalar = 0
	kindList   = 1
	kindMap    = 2
)

func (n *node) get(key string) (*node, bool) {
	if n.kind != kindMap {
		return nil, false
	}
	for _, k := range n.keys {
		v := n.m[k]
		if k == key {
			return &v, true
		}
	}
	return nil, false
}

func (n *node) scalarOrEmpty(key string) string {
	if v, ok := n.get(key); ok && v.kind == kindScalar {
		return v.scalar
	}
	return ""
}

func (n *node) listOrEmpty(key string) []node {
	if v, ok := n.get(key); ok && v.kind == kindList {
		return v.list
	}
	return nil
}

// --- parsing ---------------------------------------------------------------

var kindNames = map[int]string{kindScalar: "scalar", kindList: "list", kindMap: "map"}

// Parse validates and decodes one recipe.
func Parse(src string) (*Recipe, error) {
	lines, err := normalize(src)
	if err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return nil, fmt.Errorf("empty recipe")
	}
	p := &parser{lines: lines}
	root, err := p.block(lines[0].ind)
	if err != nil {
		return nil, err
	}
	if p.pos < len(p.lines) {
		return nil, fmt.Errorf("line %d: unexpected %q outside the top-level block", p.pos+1, p.lines[p.pos].text)
	}
	if root.kind != kindMap {
		return nil, fmt.Errorf("top level must be a mapping (key: value), got %s", kindNames[root.kind])
	}
	return fromNode(root)
}

type line struct {
	ind  int    // indent in spaces
	text string // comment-stripped, trimmed
}

func normalize(src string) ([]line, error) {
	var out []line
	for i, raw := range strings.Split(src, "\n") {
		if strings.Contains(raw, "\t") {
			return nil, fmt.Errorf("line %d: tabs are not allowed, indent with spaces", i+1)
		}
		s := stripComment(raw)
		if strings.TrimSpace(s) == "" {
			continue
		}
		ind := len(s) - len(strings.TrimLeft(s, " "))
		out = append(out, line{ind: ind, text: strings.TrimSpace(s)})
	}
	return out, nil
}

// stripComment removes "# …" comments (never inside values the subset uses,
// so a naive split is fine — keys/values here don't contain '#').
func stripComment(raw string) string {
	if i := strings.Index(raw, "#"); i >= 0 {
		return raw[:i]
	}
	return raw
}

type parser struct {
	lines []line
	pos   int
}

func (p *parser) block(indent int) (node, error) {
	l := p.lines[p.pos]
	if l.ind < indent {
		return node{}, fmt.Errorf("line %d: inconsistent indent", p.pos+1)
	}
	if strings.HasPrefix(l.text, "- ") || l.text == "-" {
		list := node{kind: kindList, list: []node{}}
		for p.pos < len(p.lines) {
			l := p.lines[p.pos]
			if l.ind < indent {
				break
			}
			if l.ind > indent || !(strings.HasPrefix(l.text, "- ") || l.text == "-") {
				return node{}, fmt.Errorf("line %d: lists support scalars only", p.pos+1)
			}
			item := strings.TrimSpace(strings.TrimPrefix(l.text, "-"))
			if item == "" {
				return node{}, fmt.Errorf("line %d: empty list item", p.pos+1)
			}
			if strings.Contains(item, ": ") || strings.HasSuffix(item, ":") {
				return node{}, fmt.Errorf("line %d: nested maps inside lists are not supported", p.pos+1)
			}
			list.list = append(list.list, node{kind: kindScalar, scalar: item})
			p.pos++
		}
		return list, nil
	}
	m := node{kind: kindMap, m: map[string]node{}}
	for p.pos < len(p.lines) {
		l := p.lines[p.pos]
		if l.ind < indent {
			break
		}
		if l.ind > indent {
			return node{}, fmt.Errorf("line %d: unexpected deeper indent", p.pos+1)
		}
		body := l.text
		// keyed line
		ci := strings.Index(body, ":")
		if ci <= 0 {
			return node{}, fmt.Errorf("line %d: expected 'key: value'", p.pos+1)
		}
		key := strings.TrimSpace(body[:ci])
		val := strings.TrimSpace(body[ci+1:])
		if strings.ContainsAny(key, " -[]{}") {
			return node{}, fmt.Errorf("line %d: unsupported key %q", p.pos+1, key)
		}
		p.pos++
		if val != "" && strings.HasPrefix(val, "[") && strings.HasSuffix(val, "]") {
			items := []node{}
			for _, part := range strings.Split(strings.TrimSpace(val[1:len(val)-1]), ",") {
				part = strings.TrimSpace(part)
				if part == "" {
					continue
				}
				items = append(items, node{kind: kindScalar, scalar: part})
			}
			m.keys = append(m.keys, key)
			m.m[key] = node{kind: kindList, list: items}
			continue
		}
		if val != "" {
			if val == "-" || strings.HasPrefix(val, "- ") { // bare "- a" inline is list syntax misuse
				return node{}, fmt.Errorf("line %d: put list items on their own lines ('- x')", p.pos+1)
			}
			if strings.Contains(val, ": ") || strings.Contains(val, " [") {
				// "a: b: c" is always a mistake in this subset
				return node{}, fmt.Errorf("line %d: unsupported value %q", p.pos+1, val)
			}
			m.keys = append(m.keys, key)
			m.m[key] = node{kind: kindScalar, scalar: val}
			continue
		}
		// key: followed by nothing → nested block (or empty)
		if p.pos < len(p.lines) && p.lines[p.pos].ind > indent {
			sub, err := p.block(p.lines[p.pos].ind)
			if err != nil {
				return node{}, err
			}
			m.keys = append(m.keys, key)
			m.m[key] = sub
			continue
		}
		m.keys = append(m.keys, key)
		m.m[key] = node{kind: kindMap, m: map[string]node{}} // explicit empty
	}
	return m, nil
}

// --- decode ----------------------------------------------------------------

func fromNode(root node) (*Recipe, error) {
	r := &Recipe{Env: map[string]string{}}
	r.Name = root.scalarOrEmpty("name")
	r.Stack = root.scalarOrEmpty("stack")
	r.Egress = root.scalarOrEmpty("egress")
	r.Provider = root.scalarOrEmpty("provider")
	r.Model = root.scalarOrEmpty("model")
	r.Attach = root.scalarOrEmpty("attach")
	for _, n := range root.listOrEmpty("ports") {
		r.Ports = append(r.Ports, n.scalar)
	}
	for _, n := range root.listOrEmpty("mounts") {
		r.Mounts = append(r.Mounts, n.scalar)
	}
	for _, n := range root.listOrEmpty("egress_allowlist") {
		r.EgressAllow = append(r.EgressAllow, n.scalar)
	}
	if v, ok := root.get("dns_track"); ok && v.kind == kindScalar {
		switch strings.ToLower(v.scalar) {
		case "true", "yes", "1":
			r.DNSTrack = true
		case "false", "no", "0", "":
		default:
			return nil, fmt.Errorf("dns_track: boolean expected (got %q)", v.scalar)
		}
	}
	if v, ok := root.get("budget"); ok && v.kind == kindScalar {
		f, err := strconv.ParseFloat(v.scalar, 64)
		if err != nil || f < 0 {
			return nil, fmt.Errorf("budget: must be a non-negative number (got %q)", v.scalar)
		}
		r.Budget = &f
	} else if ok && v.kind != kindScalar {
		return nil, fmt.Errorf("budget: must be a scalar number")
	}
	if v, ok := root.get("budget_tokens"); ok && v.kind == kindScalar {
		i, err := strconv.Atoi(v.scalar)
		if err != nil || i < 0 {
			return nil, fmt.Errorf("budget_tokens: must be a non-negative integer (got %q)", v.scalar)
		}
		r.BudgetTokens = &i
	} else if ok && v.kind != kindScalar {
		return nil, fmt.Errorf("budget_tokens: must be a scalar integer")
	}
	if v, ok := root.get("sync"); ok && v.kind == kindScalar {
		switch strings.ToLower(v.scalar) {
		case "true", "yes", "1":
			r.Sync = true
		case "false", "no", "0", "":
		default:
			return nil, fmt.Errorf("sync: boolean expected (got %q)", v.scalar)
		}
	}
	if v, ok := root.get("env"); ok && v.kind == kindMap {
		for _, k := range v.keys {
			sub := v.m[k]
			if sub.kind != kindScalar {
				return nil, fmt.Errorf("env.%s: scalar required", k)
			}
			if strings.ContainsAny(k, "- ") {
				return nil, fmt.Errorf("env.%s: not a valid variable name", k)
			}
			r.Env[k] = sub.scalar
		}
	} else if ok && v.kind != kindMap {
		return nil, fmt.Errorf("env: must be a map of KEY: value")
	}
	return r, nil
}

// Validate checks cross-field consistency; nil = usable.
func (r *Recipe) Validate() error {
	if r.Name == "" {
		return fmt.Errorf("name: is required")
	}
	if strings.ContainsAny(r.Name, " /\\:$") {
		return fmt.Errorf("name %q: spaces and path characters are not allowed", r.Name)
	}
	switch r.Egress {
	case "", "allow", "proxy-only", "none", "allowlist":
	default:
		return fmt.Errorf("egress %q: use allow | proxy-only | none | allowlist", r.Egress)
	}
	if r.DNSTrack && r.Egress != "allow" && r.Egress != "allowlist" {
		return fmt.Errorf("dns_track: needs egress allow or allowlist (got %q)", r.Egress)
	}
	if len(r.EgressAllow) > 0 && r.Egress != "allowlist" {
		return fmt.Errorf("egress_allowlist: only used with egress: allowlist")
	}
	targets := map[string]bool{}
	for _, m := range r.Mounts {
		spec, err := MountSpec(m)
		if err != nil {
			return fmt.Errorf("mounts: %w", err)
		}
		if targets[spec] {
			return fmt.Errorf("mounts: duplicate container path %s", spec)
		}
		targets[spec] = true
	}
	for _, p := range r.Ports {
		if !isHostPort(p) {
			return fmt.Errorf("ports: %q must be HOST:CONTAINER (numbers or range)", p)
		}
	}
	if r.Sync && r.Provider == "" {
		return fmt.Errorf("sync: true requires provider: (the provider whose key syncs)")
	}
	return nil
}

// MountSpec validates "HOST:CT[:ro]" and normalizes surrogates.
func MountSpec(spec string) (string, error) {
	parts := strings.Split(spec, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return "", fmt.Errorf("%q must be HOST:CT[:ro|rw]", spec)
	}
	if !strings.HasPrefix(parts[0], "/") && !strings.HasPrefix(parts[0], "~") {
		return "", fmt.Errorf("%q: HOST must be absolute or ~/…", spec)
	}
	if !strings.HasPrefix(parts[1], "/") {
		return "", fmt.Errorf("%q: CT must be an absolute container path", spec)
	}
	if len(parts) == 3 {
		m := strings.ToLower(parts[2])
		if m != "ro" && m != "rw" {
			return "", fmt.Errorf("%q: mode must be ro or rw", spec)
		}
	}
	target := parts[1]
	target = strings.TrimSuffix(target, "/")
	return target, nil
}

func isHostPort(p string) bool {
	l := strings.SplitN(p, ":", 2)
	if len(l) != 2 {
		return false
	}
	for _, side := range l {
		for _, part := range strings.Split(side, "-") {
			n, err := strconv.Atoi(strings.TrimSpace(part))
			if err != nil || n <= 0 || n > 65535 {
				return false
			}
		}
	}
	return true
}
