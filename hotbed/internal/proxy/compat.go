package proxy

// Per-provider request rewrites (config: providers.<id>.compat).
//
// "anthropic-gateway" — for the Anthropic Messages route of an OpenAI-style
// gateway (Bifrost, LiteLLM, …) in front of open models. Such gateways
// translate Anthropic requests to chat completions, where two things that
// are valid Anthropic break open-model chat templates or gateway checks:
//
//   - system-role messages inside `messages` (Claude Code sends them) become
//     a system message mid-conversation; templates like Qwen's reject that
//     ("System message must be at the beginning") → moved into the
//     top-level `system` blocks, which every translator puts first;
//   - output_config.effort "high" (Claude Code's default) is not in the
//     gateway's reasoning-effort set (low|medium|xhigh) → mapped to xhigh.
//
// Never applied unless the provider opts in: real Anthropic is untouched.

import "encoding/json"

const compatAnthropicGateway = "anthropic-gateway"

// rewriteAnthropicGateway returns the rewritten body and whether anything
// changed; undecodable bodies pass through untouched.
func rewriteAnthropicGateway(body []byte) ([]byte, bool) {
	var req map[string]any
	if json.Unmarshal(body, &req) != nil {
		return body, false
	}
	changed := false

	if msgs, ok := req["messages"].([]any); ok {
		var keep []any
		var moved []any
		for _, m := range msgs {
			mm, ok := m.(map[string]any)
			if !ok || mm["role"] != "system" {
				keep = append(keep, m)
				continue
			}
			moved = append(moved, systemBlocks(mm["content"])...)
		}
		if len(moved) > 0 {
			sys := systemBlocks(req["system"])
			req["system"] = append(sys, moved...)
			req["messages"] = keep
			changed = true
		}
	}

	if oc, ok := req["output_config"].(map[string]any); ok && oc["effort"] == "high" {
		oc["effort"] = "xhigh"
		changed = true
	}

	if !changed {
		return body, false
	}
	out, err := json.Marshal(req)
	if err != nil {
		return body, false
	}
	return out, true
}

// systemBlocks normalizes a system value (string or block list) to blocks.
func systemBlocks(v any) []any {
	switch s := v.(type) {
	case nil:
		return []any{}
	case string:
		if s == "" {
			return []any{}
		}
		return []any{map[string]any{"type": "text", "text": s}}
	case []any:
		return s
	}
	return []any{}
}
