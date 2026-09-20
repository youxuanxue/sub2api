package logredact

import (
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
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
	keySuffixes keySuffixNode
	// Simple keys can be redacted by the linear scanner below. Unusual
	// configured keys keep the conservative regexp path for compatibility.
	linearAssignments bool
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
	redacted, err := RedactJSONValue(raw, extraKeys...)
	if err != nil {
		return "<non-json payload redacted>"
	}
	encoded, err := json.Marshal(redacted)
	if err != nil {
		return "<redacted>"
	}
	return string(encoded)
}

// RedactJSONValue decodes and redacts a JSON payload once, returning the
// resulting Go value. Callers that already need a decoded value can avoid the
// marshal followed by a second unmarshal required by RedactJSON.
func RedactJSONValue(raw []byte, extraKeys ...string) (any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	keys := buildKeySet(extraKeys)
	return redactValueWithDepth(value, keys, getTextRedactPatterns(extraKeys), 0), nil
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

// RedactSSE preserves SSE framing and takes the structured path for an event
// with exactly one JSON data line. Unknown or partial events retain the
// established text redaction path, which still covers known token shapes and
// sensitive assignments without scanning unrelated prose with the regex chain.
func RedactSSE(input string, extraKeys ...string) string {
	if input == "" {
		return ""
	}
	patterns := getTextRedactPatterns(extraKeys)
	if !patterns.mayContainAssignment(input) && !mayContainKnownToken(input) {
		return input
	}
	var b strings.Builder
	b.Grow(len(input))
	for len(input) > 0 {
		separatorStart, separatorLen := sseEventSeparator(input)
		if separatorStart < 0 {
			_, _ = b.WriteString(redactSSEEvent(input, extraKeys...))
			break
		}
		_, _ = b.WriteString(redactSSEEvent(input[:separatorStart], extraKeys...))
		_, _ = b.WriteString(input[separatorStart : separatorStart+separatorLen])
		input = input[separatorStart+separatorLen:]
	}
	return b.String()
}

func sseEventSeparator(input string) (start, length int) {
	lf := strings.Index(input, "\n\n")
	crlf := strings.Index(input, "\r\n\r\n")
	switch {
	case lf < 0 && crlf < 0:
		return -1, 0
	case crlf >= 0 && (lf < 0 || crlf < lf):
		return crlf, 4
	default:
		return lf, 2
	}
}

func redactSSEEvent(event string, extraKeys ...string) string {
	if event == "" {
		return ""
	}
	lineSeparator := "\n"
	if strings.Contains(event, "\r\n") {
		lineSeparator = "\r\n"
	}
	lines := strings.Split(event, lineSeparator)
	dataLine := -1
	dataCount := 0
	for i, line := range lines {
		if strings.HasPrefix(line, "data:") {
			dataLine = i
			dataCount++
		}
	}
	if dataCount == 1 {
		line := strings.TrimSuffix(lines[dataLine], "\r")
		payload := strings.TrimPrefix(line, "data:")
		payload = strings.TrimPrefix(payload, " ")
		patterns := getTextRedactPatterns(extraKeys)
		if json.Valid([]byte(payload)) && (patterns.mayContainAssignment(payload) || mayContainKnownToken(payload)) {
			redacted := RedactJSON([]byte(payload), extraKeys...)
			prefixLen := len("data:")
			if len(line) > prefixLen && line[prefixLen] == ' ' {
				prefixLen++
			}
			prefix := line[:prefixLen]
			lines[dataLine] = prefix + redacted
			for i := range lines {
				if i != dataLine {
					lines[i] = RedactText(lines[i], extraKeys...)
				}
			}
			return strings.Join(lines, lineSeparator)
		}
		if json.Valid([]byte(payload)) {
			for i := range lines {
				if i != dataLine {
					lines[i] = RedactText(lines[i], extraKeys...)
				}
			}
			return strings.Join(lines, lineSeparator)
		}
	}
	return RedactText(event, extraKeys...)
}

func mayContainKnownToken(input string) bool {
	return strings.Contains(input, "-----BEGIN ") ||
		strings.Contains(input, "sk-") ||
		containsGitHubTokenPrefix(input) ||
		strings.Contains(input, "github_pat_") ||
		strings.Contains(input, "glpat-") ||
		strings.Contains(input, "AKIA") ||
		strings.Contains(input, "ASIA") ||
		strings.Contains(input, "LTAI") ||
		containsBearer(input) ||
		strings.Contains(input, "GOCSPX-") ||
		strings.Contains(input, "AIza")
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
	if strings.Contains(out, "sk-") || containsGitHubTokenPrefix(out) ||
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
	if patterns.linearAssignments {
		if scanned := redactSimpleAssignments(out, patterns); scanned != out && !containsNonASCII(out) {
			return scanned
		}
	}
	if strings.Contains(out, ":") && strings.Contains(out, `"`) {
		out = patterns.reJSONLike.ReplaceAllString(out, `$1***$3`)
	}
	if strings.Contains(out, "=") {
		out = patterns.reQueryLike.ReplaceAllString(out, `$1=***`)
	}
	if strings.ContainsAny(out, ":=") {
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
	var suffixes keySuffixNode
	linearAssignments := true
	for _, key := range append(append([]string(nil), defaultSensitiveKeyList...), normalizeAndSortExtraKeys(extraKeys)...) {
		suffixes.add(key)
		if !isLinearAssignmentKey(key) {
			linearAssignments = false
		}
	}
	return &textRedactPatterns{
		keySuffixes:       suffixes,
		linearAssignments: linearAssignments,
		// JSON-like: "access_token":"..."
		reJSONLike: regexp.MustCompile(`(?i)("(?:` + keyAlt + `)"\s*:\s*")([^"]*)(")`),
		// Query-like: access_token=...
		reQueryLike: regexp.MustCompile(`(?i)\b((?:` + keyAlt + `))=([^&\s]+)`),
		// Plain: access_token: ... / access_token = ...
		rePlain: regexp.MustCompile(`(?i)\b((?:` + keyAlt + `))\b(\s*[:=]\s*)([^,\s]+)`),
	}
}

func isLinearAssignmentKey(key string) bool {
	if key == "" || !isWordByte(key[len(key)-1]) {
		return false
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		if !isWordByte(c) && c != '-' {
			return false
		}
	}
	return true
}

func isWordByte(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9') || c == '_'
}

func (p *textRedactPatterns) hasExactKey(input string) bool {
	n := &p.keySuffixes
	for len(input) > 0 {
		r, size := utf8.DecodeLastRuneInString(input)
		input = input[:len(input)-size]
		n = n.children[foldKeyRune(r)]
		if n == nil {
			return false
		}
	}
	return n.terminal
}

type assignmentReplacement struct {
	start int
	end   int
}

// redactSimpleAssignments replaces values for the common identifier-shaped
// sensitive keys in one linear pass. It preserves the old regexp behaviour for
// key names containing punctuation or non-ASCII characters.
func redactSimpleAssignments(input string, patterns *textRedactPatterns) string {
	var replacementBuffer [8]assignmentReplacement
	replacements := replacementBuffer[:0]
	for delimiter := 0; delimiter < len(input); delimiter++ {
		if input[delimiter] != ':' && input[delimiter] != '=' {
			continue
		}

		// JSON-like: "token": "value". The legacy regexp intentionally
		// ignores escaped quotes, so keep that exact conservative boundary.
		keyEnd := delimiter
		for keyEnd > 0 && isAssignmentSpace(input[keyEnd-1]) {
			keyEnd--
		}
		if keyEnd > 0 && input[keyEnd-1] == '"' {
			quoteEnd := keyEnd - 1
			quoteStart := strings.LastIndexByte(input[:quoteEnd], '"')
			if quoteStart >= 0 && patterns.hasExactKey(input[quoteStart+1:quoteEnd]) {
				valueStart := delimiter + 1
				for valueStart < len(input) && isAssignmentSpace(input[valueStart]) {
					valueStart++
				}
				if valueStart < len(input) && input[valueStart] == '"' {
					valueEnd := strings.IndexByte(input[valueStart+1:], '"')
					if valueEnd >= 0 {
						replacements = append(replacements, assignmentReplacement{valueStart + 1, valueStart + 1 + valueEnd})
						// Skip the delimiter immediately following the quoted value;
						// otherwise a malformed `"key":"value":...` sequence can
						// treat the value as a second JSON key.
						delimiter = valueStart + 1 + valueEnd + 1
						continue
					}
				}
			}
		}

		keyStart := keyEnd
		for keyStart > 0 && isLinearKeyByte(input[keyStart-1]) {
			keyStart--
		}
		candidateStart := keyStart
		if candidateStart != keyEnd && !patterns.hasExactKey(input[candidateStart:keyEnd]) {
			for i := keyStart; i < keyEnd; i++ {
				if input[i] == '-' && i+1 < keyEnd && patterns.hasExactKey(input[i+1:keyEnd]) {
					candidateStart = i + 1
					break
				}
			}
		}
		if candidateStart == keyEnd || !patterns.hasExactKey(input[candidateStart:keyEnd]) ||
			(candidateStart > 0 && isWordByte(input[candidateStart-1])) || !isWordByte(input[keyEnd-1]) {
			continue
		}

		valueStart := delimiter + 1
		queryLike := input[delimiter] == '=' && valueStart < len(input) && !isAssignmentSpace(input[valueStart])
		if !queryLike {
			for valueStart < len(input) && isAssignmentSpace(input[valueStart]) {
				valueStart++
			}
		}
		if valueStart >= len(input) || (!queryLike && input[valueStart] == ',') || isAssignmentSpace(input[valueStart]) {
			continue
		}
		valueEnd := valueStart
		if queryLike {
			for valueEnd < len(input) && input[valueEnd] != '&' && !isAssignmentSpace(input[valueEnd]) {
				valueEnd++
			}
			// The legacy query replacement runs before the plain assignment
			// replacement. For ordinary values, the latter extends across `&`
			// until a comma or whitespace; a value beginning with a comma does
			// not match that second form.
			if valueStart < len(input) && input[valueStart] != ',' {
				plainEnd := valueStart
				for plainEnd < len(input) && input[plainEnd] != ',' && !isAssignmentSpace(input[plainEnd]) {
					plainEnd++
				}
				if plainEnd > valueEnd {
					valueEnd = plainEnd
				}
			}
		} else {
			for valueEnd < len(input) && input[valueEnd] != ',' && !isAssignmentSpace(input[valueEnd]) {
				valueEnd++
			}
		}
		if valueEnd > valueStart {
			replacements = append(replacements, assignmentReplacement{valueStart, valueEnd})
			delimiter = valueEnd - 1
		}
	}
	if len(replacements) == 0 {
		return input
	}
	var b strings.Builder
	b.Grow(len(input))
	last := 0
	for _, replacement := range replacements {
		if replacement.start < last {
			continue
		}
		_, _ = b.WriteString(input[last:replacement.start])
		_, _ = b.WriteString("***")
		last = replacement.end
	}
	_, _ = b.WriteString(input[last:])
	return b.String()
}

func isLinearKeyByte(c byte) bool {
	return isWordByte(c) || c == '-'
}

func isAssignmentSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '\f'
}

func containsNonASCII(input string) bool {
	for i := 0; i < len(input); i++ {
		if input[i] >= utf8.RuneSelf {
			return true
		}
	}
	return false
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
		if p.hasKeySuffix(input[:end]) {
			return true
		}
		if end > 0 && input[end-1] == '"' && p.hasKeySuffix(input[:end-1]) {
			return true
		}
	}
	return false
}

// The reversed trie rejects unrelated assignments after their last few runes.
// Using the same Unicode simple folding as regexp avoids treating all nearby
// non-ASCII prose as a possible credential. It also supports arbitrary extra keys.
type keySuffixNode struct {
	children map[rune]*keySuffixNode
	terminal bool
}

func (n *keySuffixNode) add(key string) {
	for len(key) > 0 {
		r, size := utf8.DecodeLastRuneInString(key)
		key = key[:len(key)-size]
		r = foldKeyRune(r)
		if n.children == nil {
			n.children = make(map[rune]*keySuffixNode)
		}
		child := n.children[r]
		if child == nil {
			child = &keySuffixNode{}
			n.children[r] = child
		}
		n = child
	}
	n.terminal = true
}

func foldKeyRune(r rune) rune {
	if r < utf8.RuneSelf {
		if r >= 'a' && r <= 'z' {
			return r - ('a' - 'A')
		}
		return r
	}
	// Canonicalize the entire simple-fold cycle, including ſ/S/s and K/K/k.
	minimum := r
	for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
		minimum = min(minimum, next)
	}
	return minimum
}

func (p *textRedactPatterns) hasKeySuffix(input string) bool {
	n := &p.keySuffixes
	for len(input) > 0 {
		r, size := utf8.DecodeLastRuneInString(input)
		input = input[:len(input)-size]
		n = n.children[foldKeyRune(r)]
		if n == nil {
			return false
		}
		if n.terminal {
			return true
		}
	}
	return false
}

// "gh" also occurs in ordinary words such as "thought" and "high". Only
// dispatch the provider regexp for one of its actual case-sensitive prefixes.
func containsGitHubTokenPrefix(input string) bool {
	for {
		i := strings.Index(input, "gh")
		if i < 0 {
			return false
		}
		input = input[i:]
		if len(input) >= 4 && input[3] == '_' {
			switch input[2] {
			case 'p', 'o', 'u', 's', 'r':
				return true
			}
		}
		input = input[2:]
	}
}

func getTextRedactPatterns(extraKeys []string) *textRedactPatterns {
	normalizedExtraKeys := normalizeAndSortExtraKeys(extraKeys)
	if len(normalizedExtraKeys) == 0 {
		return defaultTextRedactPatterns
	}

	// Length prefixes keep arbitrary key contents from colliding with list separators.
	var encodedKeys []byte
	for _, key := range normalizedExtraKeys {
		encodedKeys = strconv.AppendInt(encodedKeys, int64(len(key)), 10)
		encodedKeys = append(encodedKeys, ':')
		encodedKeys = append(encodedKeys, key...)
	}
	cacheKey := string(encodedKeys)
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
