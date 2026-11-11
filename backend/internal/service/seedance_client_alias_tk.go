package service

import (
	"strings"

	"github.com/tidwall/sjson"
)

// 1.5-pro spellings still collapse to the dated client that XRToken / Ark /
// FMGo map. There is no VolcEngine plan identity for 1.5-pro.
//
// 2.0, 2.0-fast, and 2.5 hyphen spellings collapse to the dotted plan ids.
// Those ids are separately priced and mapped on VolcEngine plan accounts.
// Dated whitelist ids pass through, so a client that sends one still reaches
// XRToken / Ark / FMGo. Applied before candidate evaluation and the
// unpriced-media guard.
var seedanceClientAliases = map[string]string{
	"doubao-seedance-1-5-pro":  "doubao-seedance-1-5-pro-251215",
	"doubao-seedance-1.5-pro":  "doubao-seedance-1-5-pro-251215",
	"doubao-seedance-2-0":      "doubao-seedance-2.0",
	"doubao-seedance-2-0-fast": "doubao-seedance-2.0-fast",
	"doubao-seedance-2-5":      "doubao-seedance-2.5",
}

// ResolveSeedanceClientAlias maps a TokenKey public alias onto the id that
// account selection should see. Dated whitelist ids, VolcEngine plan dotted
// ids, mini, and unknown names pass through.
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
