package service

import "strings"

// canonicalGeminiServableTrafficModel rewrites a withdrawn Gemini client id
// onto the servable wire id used for mapping lookup and upstream resolution.
// It does not price the request and does not insert the withdrawn id into
// persisted account mappings. Unknown names, including other *-preview ids,
// are returned unchanged. The alias table is geminiTrafficAliases.
func canonicalGeminiServableTrafficModel(model string) string {
	raw := strings.TrimSpace(model)
	if raw == "" {
		return ""
	}
	id := raw
	if rest, ok := strings.CutPrefix(id, "models/"); ok {
		id = strings.TrimSpace(rest)
	}
	target, ok := geminiTrafficAliases[id]
	if !ok || target == "" || target == id {
		return raw
	}
	return target
}
