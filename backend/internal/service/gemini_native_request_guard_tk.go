package service

import (
	"strings"

	"github.com/tidwall/gjson"
)

// NativeGeminiGenerateContentEnvelopeError reports a native generateContent body
// that is not a Gemini request. Foreign top-level keys are the Chat, Messages,
// and Responses envelopes; they are rejected before an account is selected.
// Nested keys with the same names do not count.
func NativeGeminiGenerateContentEnvelopeError(body []byte) string {
	root := gjson.ParseBytes(body)
	if !gjson.ValidBytes(body) || !root.IsObject() {
		return "Request body must be a JSON object"
	}
	for _, key := range []string{"messages", "max_tokens", "input", "store"} {
		if root.Get(key).Exists() {
			return "This path only accepts a Gemini generateContent body"
		}
	}
	contents := root.Get("contents")
	if !contents.IsArray() || len(contents.Array()) == 0 {
		return "Gemini generateContent requires a non-empty contents array"
	}
	return ""
}

// GeminiBodyHasRemoteFileURI reports a fileData/file_data fileUri that
// Antigravity cannot fetch. inlineData is allowed. Other URI schemes are left
// for the upstream to judge.
func GeminiBodyHasRemoteFileURI(body []byte) bool {
	if !gjson.ValidBytes(body) {
		return false
	}
	return geminiValueHasRemoteFileURI(gjson.ParseBytes(body))
}

func geminiValueHasRemoteFileURI(v gjson.Result) bool {
	if v.IsArray() {
		for _, item := range v.Array() {
			if geminiValueHasRemoteFileURI(item) {
				return true
			}
		}
		return false
	}
	if !v.IsObject() {
		return false
	}
	for _, key := range []string{"fileData", "file_data"} {
		file := v.Get(key)
		if !file.Exists() {
			continue
		}
		uri := strings.TrimSpace(file.Get("fileUri").String())
		if uri == "" {
			uri = strings.TrimSpace(file.Get("file_uri").String())
		}
		if geminiRemoteFileURI(uri) {
			return true
		}
	}
	found := false
	v.ForEach(func(key, value gjson.Result) bool {
		if key.String() == "fileData" || key.String() == "file_data" {
			return true
		}
		if geminiValueHasRemoteFileURI(value) {
			found = true
			return false
		}
		return true
	})
	return found
}

func geminiRemoteFileURI(uri string) bool {
	low := strings.ToLower(strings.TrimSpace(uri))
	return strings.HasPrefix(low, "gs://") || strings.HasPrefix(low, "http://") || strings.HasPrefix(low, "https://")
}

// antigravityRemoteFileRejected rejects a remote fileUri on the transport that
// would send it. Governed Antigravity accounts execute Gemini identity, so the
// check has to live in ForwardGemini and ForwardNative rather than the
// non-governed branch.
func antigravityRemoteFileRejected(account *Account, action string, body []byte) error {
	if account == nil || account.Platform != PlatformAntigravity {
		return nil
	}
	if action != "generateContent" && action != "streamGenerateContent" {
		return nil
	}
	if !GeminiBodyHasRemoteFileURI(body) {
		return nil
	}
	return &AntigravityInlineMediaError{}
}

// AntigravityInlineMediaError is a local 400. The native body never reaches
// Antigravity when it references a remote or GCS file.
type AntigravityInlineMediaError struct{}

func (AntigravityInlineMediaError) Error() string {
	return "Antigravity Gemini only accepts inlineData; remote fileUri is not supported"
}
