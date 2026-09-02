package service

import (
	"strings"

	"github.com/tidwall/sjson"
)

// TokenKey-facing Seedance aliases collapse to the dated official client ids
// already priced and mapped. Applied at the video submit throat before the
// unpriced-media guard and account selection, so XRToken / Ark / FMGo keep
// serving the dated whitelist keys. Aliases stay off the public catalog /
// Studio menu.
var seedanceClientAliases = map[string]string{
	"doubao-seedance-2-0":      "doubao-seedance-2-0-260128",
	"doubao-seedance-2.0":      "doubao-seedance-2-0-260128",
	"doubao-seedance-2-0-fast": "doubao-seedance-2-0-fast-260128",
	"doubao-seedance-2.0-fast": "doubao-seedance-2-0-fast-260128",
	"doubao-seedance-2-5":      "doubao-seedance-2-5-260628",
	"doubao-seedance-2.5":      "doubao-seedance-2-5-260628",
}

// ResolveSeedanceClientAlias maps a TokenKey public alias to its dated
// official client. Dated ids, mini, and unknown names pass through.
func ResolveSeedanceClientAlias(model string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(model))
	if normalized == "" {
		return "", false
	}
	resolved, ok := seedanceClientAliases[normalized]
	return resolved, ok
}

// ApplySeedanceClientAlias rewrites body.model when the requested id is a
// public Seedance alias. On a miss it returns the original body unchanged.
func ApplySeedanceClientAlias(body []byte, model string) ([]byte, string, bool) {
	resolved, ok := ResolveSeedanceClientAlias(model)
	if !ok {
		return body, strings.TrimSpace(model), false
	}
	rewritten, err := sjson.SetBytes(body, "model", resolved)
	if err != nil {
		return body, strings.TrimSpace(model), false
	}
	return rewritten, resolved, true
}
