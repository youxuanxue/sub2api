package service

import (
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// CleanGeminiNativeThoughtSignatures 从 Gemini 原生 API 请求中替换 thoughtSignature 字段为 dummy 签名，
// 以避免跨账号签名验证错误。
//
// 当粘性会话切换账号时（例如原账号异常、不可调度等），旧账号返回的 thoughtSignature
// 会导致新账号的签名验证失败。通过替换为 dummy 签名，跳过签名验证。
//
// CleanGeminiNativeThoughtSignatures replaces thoughtSignature fields with dummy signature
// in Gemini native API requests to avoid cross-account signature validation errors.
//
// When sticky session switches accounts (e.g., original account becomes unavailable),
// thoughtSignatures from the old account will cause validation failures on the new account.
// By replacing with dummy signature, we skip signature validation.
func CleanGeminiNativeThoughtSignatures(body []byte) []byte {
	if !gjson.ValidBytes(body) {
		return body
	}
	result := body
	// Tool data is opaque. Targeted edits preserve numbers without a float64 round trip.
	for i, content := range gjson.GetBytes(body, "contents").Array() {
		for j, part := range content.Get("parts").Array() {
			signature := part.Get("thoughtSignature")
			if !signature.Exists() || signature.String() == antigravity.DummyThoughtSignature {
				continue
			}
			path := "contents." + strconv.Itoa(i) + ".parts." + strconv.Itoa(j) + ".thoughtSignature"
			updated, err := sjson.SetBytes(result, path, antigravity.DummyThoughtSignature)
			if err != nil {
				return body
			}
			result = updated
		}
	}
	return result
}
