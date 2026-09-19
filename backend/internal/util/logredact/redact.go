package logredact

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// maxRedactDepth 限制递归深度以防止栈溢出
const maxRedactDepth = 32

var defaultSensitiveKeys = map[string]struct{}{
	"authorization":         {},
	"proxy-authorization":   {},
	"x-api-key":             {},
	"api-key":               {},
	"api_key":               {},
	"apikey":                {},
	"authorization_code":    {},
	"code":                  {},
	"code_verifier":         {},
	"access_token":          {},
	"refresh_token":         {},
	"id_token":              {},
	"session_token":         {},
	"bearer_token":          {},
	"token":                 {},
	"jwt":                   {},
	"password":              {},
	"passwd":                {},
	"passphrase":            {},
	"secret":                {},
	"client_secret":         {},
	"private_key":           {},
	"privatekey":            {},
	"accesskeysecret":       {},
	"accesskeyid":           {},
	"secretaccesskey":       {},
	"aws_access_key_id":     {},
	"aws_secret_access_key": {},
	"cookie":                {},
	"set-cookie":            {},
	"credential":            {},
	"credentials":           {},
	"signature":             {},
}

var defaultSensitiveKeyList = []string{
	"access_token",
	"accesskeyid",
	"accesskeysecret",
	"api-key",
	"api_key",
	"apikey",
	"authorization",
	"authorization_code",
	"aws_access_key_id",
	"aws_secret_access_key",
	"bearer_token",
	"client_secret",
	"code",
	"code_verifier",
	"cookie",
	"credential",
	"credentials",
	"id_token",
	"jwt",
	"passphrase",
	"passwd",
	"password",
	"private_key",
	"privatekey",
	"proxy-authorization",
	"refresh_token",
	"secret",
	"secretaccesskey",
	"session_token",
	"set-cookie",
	"signature",
	"token",
	"x-api-key",
}

var defaultSensitiveKeySuffixes = []string{
	"_api_key",
	"-api-key",
	"_secret",
	"-secret",
	"_token",
	"-token",
	"_password",
	"-password",
	"_passwd",
	"-passwd",
	"_passphrase",
	"-passphrase",
	"_private_key",
	"-private-key",
	"_credential",
	"-credential",
	"_credentials",
	"-credentials",
	"_signature",
	"-signature",
}

var defaultNonCredentialKeys = map[string]struct{}{
	"max_tokens":                  {},
	"max_output_tokens":           {},
	"max_input_tokens":            {},
	"max_completion_tokens":       {},
	"max_tokens_to_sample":        {},
	"budget_tokens":               {},
	"prompt_tokens":               {},
	"completion_tokens":           {},
	"input_tokens":                {},
	"output_tokens":               {},
	"total_tokens":                {},
	"token_count":                 {},
	"cache_creation_input_tokens": {},
	"cache_read_input_tokens":     {},
}

type textRedactPatterns struct {
	reJSONLike  *regexp.Regexp
	reQueryLike *regexp.Regexp
	rePlain     *regexp.Regexp
	keyHints    []string
	maxKeyBytes int
	// Keys containing punctuation or non-ASCII characters need the old
	// conservative assignment check: the fast classifier below intentionally
	// models the common identifier-like key shape only.
	conservativeAssignmentScan bool
}

var (
	reGOCSPX        = regexp.MustCompile(`GOCSPX-[0-9A-Za-z_-]{24,}`)
	reAIza          = regexp.MustCompile(`AIza[0-9A-Za-z_-]{35}`)
	rePrivateKey    = regexp.MustCompile(`(?s)-----BEGIN (?:[A-Z0-9]+ )?PRIVATE KEY-----.*?(?:-----END (?:[A-Z0-9]+ )?PRIVATE KEY-----|$)`)
	reProviderToken = regexp.MustCompile(`\b(?:glpat-[A-Za-z0-9_-]{16,}|gh[pousr]_[A-Za-z0-9_]{20,}|github_pat_[A-Za-z0-9_]{20,}|sk-[A-Za-z0-9_-]{20,}|(?:AKIA|ASIA)[A-Z0-9]{16}|LTAI[A-Za-z0-9]{12,})`)
	reBearer        = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]+`)

	defaultTextRedactPatterns = compileTextRedactPatterns(nil)
	extraTextPatternCache     sync.Map // map[string]*textRedactPatterns
)

func RedactMap(input map[string]any, extraKeys ...string) map[string]any {
	if input == nil {
		return map[string]any{}
	}
	keys := buildKeySet(extraKeys)
	redacted, ok := redactValueWithDepth(input, keys, getTextRedactPatterns(extraKeys), 0).(map[string]any)
	if !ok {
		return map[string]any{}
	}
	return redacted
}

func RedactJSON(raw []byte, extraKeys ...string) string {
	if len(raw) == 0 {
		return ""
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "<non-json payload redacted>"
	}
	keys := buildKeySet(extraKeys)
	redacted := redactValueWithDepth(value, keys, getTextRedactPatterns(extraKeys), 0)
	encoded, err := json.Marshal(redacted)
	if err != nil {
		return "<redacted>"
	}
	return string(encoded)
}

// RedactText 对非结构化文本做轻量脱敏。
//
// 规则：
// - 如果文本本身是 JSON，则按 RedactJSON 处理。
// - 否则尝试对常见 key=value / key:"value" 片段做脱敏。
//
// 注意：该函数用于日志/错误信息兜底，不保证覆盖所有格式。
func RedactText(input string, extraKeys ...string) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return ""
	}

	raw := []byte(input)
	if json.Valid(raw) {
		return RedactJSON(raw, extraKeys...)
	}

	return redactUnstructuredText(input, getTextRedactPatterns(extraKeys))
}

// Do not parse JSON here: JSON strings also pass through this function, and
// reparsing a quoted scalar would recurse indefinitely. Preserve ordinary text
// whitespace; only RedactText's legacy top-level API trims its input.
func redactUnstructuredText(input string, patterns *textRedactPatterns) string {
	out := input
	// Each guard checks a necessary literal from its regexp, not a guess about
	// what a credential looks like. Keep the replacement order and check the
	// current output: earlier replacements can expose later matches.
	if strings.Contains(out, "-----BEGIN ") {
		out = rePrivateKey.ReplaceAllString(out, "<private key redacted>")
	}
	if strings.Contains(out, "sk-") || strings.Contains(out, "gh") ||
		strings.Contains(out, "github_pat_") ||
		strings.Contains(out, "glpat-") || strings.Contains(out, "AKIA") ||
		strings.Contains(out, "ASIA") || strings.Contains(out, "LTAI") {
		out = reProviderToken.ReplaceAllString(out, "***")
	}
	if containsBearer(out) {
		out = reBearer.ReplaceAllString(out, "Bearer ***")
	}
	if strings.Contains(out, "GOCSPX-") {
		out = reGOCSPX.ReplaceAllString(out, "GOCSPX-***")
	}
	if strings.Contains(out, "AIza") {
		out = reAIza.ReplaceAllString(out, "AIza***")
	}
	if !patterns.mayContainAssignment(out) {
		return out
	}
	jsonLike, queryLike, plainLike := patterns.assignmentKindsAfterMatch(out)
	if !jsonLike && !queryLike && !plainLike {
		return out
	}
	if jsonLike {
		out = patterns.reJSONLike.ReplaceAllString(out, `$1***$3`)
	}
	if queryLike {
		out = patterns.reQueryLike.ReplaceAllString(out, `$1=***`)
	}
	if plainLike {
		out = patterns.rePlain.ReplaceAllString(out, `$1$2***`)
	}
	return out
}

func containsBearer(input string) bool {
	// Bearer's letters have no non-ASCII Unicode case-fold equivalents.
	// Avoid allocating a lowercase copy of every content string.
	for len(input) >= len("bearer") {
		i := strings.IndexAny(input, "bB")
		if i < 0 || len(input)-i < len("bearer") {
			return false
		}
		if strings.EqualFold(input[i:i+len("bearer")], "bearer") {
			return true
		}
		input = input[i+1:]
	}
	return false
}

func compileTextRedactPatterns(extraKeys []string) *textRedactPatterns {
	keyAlt := buildKeyAlternation(extraKeys)
	keyHints := append(append([]string(nil), defaultSensitiveKeyList...), normalizeAndSortExtraKeys(extraKeys)...)
	maxKeyBytes := 0
	conservativeAssignmentScan := false
	for _, key := range keyHints {
		maxKeyBytes = max(maxKeyBytes, len(key))
		for i := 0; i < len(key); i++ {
			c := key[i]
			if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') &&
				(c < '0' || c > '9') && c != '_' && c != '-' {
				conservativeAssignmentScan = true
				break
			}
		}
	}
	return &textRedactPatterns{
		keyHints:                   keyHints,
		maxKeyBytes:                maxKeyBytes,
		conservativeAssignmentScan: conservativeAssignmentScan,
		// JSON-like: "access_token":"..."
		reJSONLike: regexp.MustCompile(`(?i)("(?:` + keyAlt + `)"\s*:\s*")([^"]*)(")`),
		// Query-like: access_token=...
		reQueryLike: regexp.MustCompile(`(?i)\b((?:` + keyAlt + `))=([^&\s]+)`),
		// Plain: access_token: ... / access_token = ...
		rePlain: regexp.MustCompile(`(?i)\b((?:` + keyAlt + `))\b(\s*[:=]\s*)([^,\s]+)`),
	}
}

// assignmentKindsAfterMatch classifies the assignment forms that can match the three
// key redaction regexps. It intentionally only returns false when the common
// identifier-like key shape cannot match; unusual keys use the conservative
// path so custom-key coverage remains unchanged. The caller has already
// passed mayContainAssignment, so ordinary prose does not pay for a second
// key scan.
func (p *textRedactPatterns) assignmentKindsAfterMatch(input string) (jsonLike, queryLike, plainLike bool) {
	if p.conservativeAssignmentScan {
		return strings.Contains(input, `"`), strings.Contains(input, "="), true
	}

	for offset := 0; offset < len(input); {
		i := strings.IndexAny(input[offset:], ":=")
		if i < 0 {
			break
		}
		delim := offset + i
		keyEnd := delim
		for keyEnd > 0 && strings.ContainsRune(" \t\r\n\f", rune(input[keyEnd-1])) {
			keyEnd--
		}
		for j := max(0, keyEnd-4*p.maxKeyBytes-1); j < keyEnd; j++ {
			if input[j] >= 0x80 {
				return strings.Contains(input, `"`), strings.Contains(input, "="), true
			}
		}
		keySuffixEnd := keyEnd
		if keySuffixEnd > 0 && input[keySuffixEnd-1] == '"' {
			keySuffixEnd--
		}
		if !p.hasKeySuffix(input[:keySuffixEnd]) {
			offset = delim + 1
			continue
		}

		valueStart := delim + 1
		for valueStart < len(input) && strings.ContainsRune(" \t\r\n\f", rune(input[valueStart])) {
			valueStart++
		}
		if valueStart >= len(input) || input[valueStart] == ',' {
			offset = delim + 1
			continue
		}

		if input[delim] == ':' && keyEnd > 0 && input[keyEnd-1] == '"' && input[valueStart] == '"' {
			jsonLike = true
			// A quoted key is not a match for the plain form because the
			// regexp requires a word boundary directly after the key.
			if jsonLike && queryLike && plainLike {
				return
			}
			offset = delim + 1
			continue
		}

		if input[delim] == '=' && keyEnd == delim && input[delim+1] != '&' &&
			!strings.ContainsRune(" \t\r\n\f", rune(input[delim+1])) {
			queryLike = true
			// The query regexp stops at '&'; the plain regexp is still
			// needed to collapse the remainder of a query-like fragment,
			// matching the legacy replacement order.
			segmentEnd := len(input)
			if i := strings.IndexAny(input[valueStart:], " \t\r\n\f"); i >= 0 {
				segmentEnd = valueStart + i
			}
			if strings.Contains(input[valueStart:segmentEnd], "&") {
				plainLike = true
			}
		} else {
			plainLike = true
		}
		if jsonLike && queryLike && plainLike {
			return
		}
		offset = delim + 1
	}
	return
}

// All key regexps require a sensitive key immediately before a : or =,
// allowing whitespace and (for JSON-like text) a closing quote. Checking those
// suffixes avoids running the large alternations over ordinary prose/code.
func (p *textRedactPatterns) mayContainAssignment(input string) bool {
	for offset := 0; offset < len(input); {
		i := strings.IndexAny(input[offset:], ":=")
		if i < 0 {
			return false
		}
		end := offset + i
		offset = end + 1
		for end > 0 && strings.ContainsRune(" \t\r\n\f", rune(input[end-1])) {
			end--
		}
		// Unicode regexp folding can change byte lengths (e.g. ſecret).
		// Fall back conservatively if any possible key contains non-ASCII.
		for j := max(0, end-4*p.maxKeyBytes-1); j < end; j++ {
			if input[j] >= 0x80 {
				return true
			}
		}
		if p.hasKeySuffix(input[:end]) {
			return true
		}
		if end > 0 && input[end-1] == '"' && p.hasKeySuffix(input[:end-1]) {
			return true
		}
	}
	return false
}

func (p *textRedactPatterns) hasKeySuffix(input string) bool {
	for _, key := range p.keyHints {
		if len(input) >= len(key) && strings.EqualFold(input[len(input)-len(key):], key) {
			return true
		}
	}
	return false
}

func getTextRedactPatterns(extraKeys []string) *textRedactPatterns {
	normalizedExtraKeys := normalizeAndSortExtraKeys(extraKeys)
	if len(normalizedExtraKeys) == 0 {
		return defaultTextRedactPatterns
	}

	cacheKey := strings.Join(normalizedExtraKeys, ",")
	if cached, ok := extraTextPatternCache.Load(cacheKey); ok {
		if patterns, ok := cached.(*textRedactPatterns); ok {
			return patterns
		}
	}

	compiled := compileTextRedactPatterns(normalizedExtraKeys)
	actual, _ := extraTextPatternCache.LoadOrStore(cacheKey, compiled)
	if patterns, ok := actual.(*textRedactPatterns); ok {
		return patterns
	}
	return compiled
}

func normalizeAndSortExtraKeys(extraKeys []string) []string {
	if len(extraKeys) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(extraKeys))
	keys := make([]string, 0, len(extraKeys))
	for _, key := range extraKeys {
		normalized := normalizeKey(key)
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		keys = append(keys, normalized)
	}
	sort.Strings(keys)
	return keys
}

func buildKeyAlternation(extraKeys []string) string {
	seen := make(map[string]struct{}, len(defaultSensitiveKeyList)+len(extraKeys))
	keys := make([]string, 0, len(defaultSensitiveKeyList)+len(extraKeys))
	for _, k := range defaultSensitiveKeyList {
		seen[k] = struct{}{}
		keys = append(keys, regexp.QuoteMeta(k))
	}
	for _, k := range extraKeys {
		n := normalizeKey(k)
		if n == "" {
			continue
		}
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		keys = append(keys, regexp.QuoteMeta(n))
	}
	return strings.Join(keys, "|")
}

func buildKeySet(extraKeys []string) map[string]struct{} {
	keys := make(map[string]struct{}, len(defaultSensitiveKeys)+len(extraKeys))
	for k := range defaultSensitiveKeys {
		keys[k] = struct{}{}
	}
	for _, key := range extraKeys {
		normalized := normalizeKey(key)
		if normalized == "" {
			continue
		}
		keys[normalized] = struct{}{}
	}
	return keys
}

func redactValueWithDepth(value any, keys map[string]struct{}, patterns *textRedactPatterns, depth int) any {
	if depth > maxRedactDepth {
		return "<depth limit exceeded>"
	}

	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, val := range v {
			if isSensitiveKey(k, keys) {
				out[k] = "***"
				continue
			}
			out[k] = redactValueWithDepth(val, keys, patterns, depth+1)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = redactValueWithDepth(item, keys, patterns, depth+1)
		}
		return out
	case string:
		return redactUnstructuredText(v, patterns)
	default:
		return value
	}
}

func isSensitiveKey(key string, keys map[string]struct{}) bool {
	normalized := normalizeKey(key)
	if _, ok := defaultNonCredentialKeys[normalized]; ok {
		return false
	}
	if _, ok := keys[normalized]; ok {
		return true
	}
	for _, suffix := range defaultSensitiveKeySuffixes {
		if strings.HasSuffix(normalized, suffix) {
			return true
		}
	}
	return false
}

func normalizeKey(key string) string {
	return strings.ToLower(strings.TrimSpace(key))
}
