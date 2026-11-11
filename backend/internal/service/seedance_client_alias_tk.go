package service

import (
	"strings"

	"github.com/tidwall/sjson"
)

// Hyphen short names and the 1.5-pro dotted spelling collapse to the dated
// official client ids that XRToken / Ark / FMGo map and price. Applied before
// candidate evaluation and the unpriced-media guard, so those accounts still
// see their whitelist keys.
//
// VolcEngine plan identity ids stay unchanged: doubao-seedance-2.0,
// doubao-seedance-2.0-fast, and doubao-seedance-2.5 are separately priced and
// mapped on plan accounts. Rewriting them to the dated SKU before selection
// hides that pool.
var seedanceClientAliases = map[string]string{
	"doubao-seedance-1-5-pro":  "doubao-seedance-1-5-pro-251215",
	"doubao-seedance-1.5-pro":  "doubao-seedance-1-5-pro-251215",
	"doubao-seedance-2-0":      "doubao-seedance-2-0-260128",
	"doubao-seedance-2-0-fast": "doubao-seedance-2-0-fast-260128",
	"doubao-seedance-2-5":      "doubao-seedance-2-5-260628",
}

// ResolveSeedanceClientAlias maps a TokenKey public alias to its dated
// official client. Dated ids, VolcEngine plan dotted ids, mini, and unknown
// names pass through.
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
