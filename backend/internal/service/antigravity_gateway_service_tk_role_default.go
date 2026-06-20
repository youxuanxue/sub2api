package service

import "encoding/json"

// tkEnsureGeminiContentRoles defaults a missing `role` on each `contents[]`
// entry of a native Gemini generateContent body to "user".
//
// Why this exists (root cause, prod-confirmed 2026-06-20):
//
// TokenKey's /antigravity/v1beta/models/{model}:generateContent surface forwards
// to Google cloudcode-pa, which is the Vertex-side (Gemini Enterprise / v1internal)
// endpoint — NOT the public AI Studio Gemini API. On the public Gemini API the
// `role` field on a Content is OPTIONAL for single-turn requests (the server
// infers "user"); on the Vertex / cloudcode-pa side it is REQUIRED even for a
// single turn. A request body coded to the public spec, e.g.
//
//	{"contents":[{"parts":[{"text":"Reply OK"}]}]}
//
// therefore comes back from cloudcode-pa as:
//
//	400 {"error":{"code":400,"message":"Request contains an invalid argument.","status":"INVALID_ARGUMENT"}}
//
// Live-fire proof on prod account 62 (gemini-3-flash-agent), same body minus/plus role:
//   - {"contents":[{"parts":[...]}]}                 -> 400 INVALID_ARGUMENT
//   - {"contents":[{"role":"user","parts":[...]}]}   -> 200 OK
//   - generationConfig present but role absent       -> 400 (role, not gencfg, is the gate)
//
// Real clients (the Antigravity IDE, the Gemini SDKs, python-requests examples)
// always emit `role`, which is why native traffic that carries it succeeds
// (gemini-2.5-flash-lite: 8478 served-200 via /v1beta/models). Defaulting the
// missing role to the value every real client already sends is therefore
// fingerprint-neutral: it produces exactly the bytes a compliant client would
// have sent, never altering an explicitly-provided role.
//
// On any parse problem the original body is returned unchanged so the existing
// downstream handling (and upstream's own error) is preserved.
func tkEnsureGeminiContentRoles(body []byte) []byte {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return body
	}

	contents, ok := payload["contents"].([]any)
	if !ok || len(contents) == 0 {
		return body
	}

	modified := false
	for _, item := range contents {
		content, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role, hasRole := content["role"]
		if hasRole {
			if roleStr, ok := role.(string); ok && roleStr != "" {
				continue
			}
		}
		content["role"] = "user"
		modified = true
	}

	if !modified {
		return body
	}

	patched, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return patched
}
