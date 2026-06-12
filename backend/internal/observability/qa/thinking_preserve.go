package qa

import (
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// 背景：logredact 把任何名为 `signature` 的字段值脱敏成 "***"，因为它默认按凭证处理。
// 但 Anthropic extended-thinking 的 thinking 块自带一个 `signature`（模型完整性令牌，
// 多轮续推时必须原样回传，不是用户凭证）。对 traj-opt-in 的 Anthropic 捕获，我们要把
// **thinking 块内**的 signature 原样保留，且**仅限 thinking 块**——其它任何 signature 仍脱敏。
//
// 实现方式：结构化「脱敏后回填」——先走正常脱敏，再从原始 body 里按 thinking 块路径把
// 真实 signature 写回去。绝不全局豁免 `signature` 键，避免别处同名凭证泄露。

// isAnthropicThinkingOptIn 判定是否对该记录启用 thinking signature 保留：
// 必须同时是 Anthropic 平台 + traj/synth opt-in 记录。
func isAnthropicThinkingOptIn(platform string, dialogSynth bool) bool {
	return dialogSynth && strings.EqualFold(strings.TrimSpace(platform), "anthropic")
}

// restoreThinkingSignatures 把 original 中 thinking 块的真实 signature 回填进
// 已脱敏的 redacted JSON 字符串。处理 Anthropic wire 格式里两种承载 thinking 块的形状：
//   - 响应体：顶层 content[]
//   - 请求体：messages[].content[]
func restoreThinkingSignatures(redacted string, original []byte) string {
	if redacted == "" || len(original) == 0 {
		return redacted
	}
	orig := gjson.ParseBytes(original)
	if !orig.IsObject() {
		return redacted
	}
	out := redacted
	// 响应形状：content[]
	out = restoreSignaturesInContentArray(out, "content", orig.Get("content"))
	// 请求形状：messages[].content[]
	if msgs := orig.Get("messages"); msgs.IsArray() {
		msgIdx := 0
		msgs.ForEach(func(_, msg gjson.Result) bool {
			prefix := "messages." + strconv.Itoa(msgIdx) + ".content"
			out = restoreSignaturesInContentArray(out, prefix, msg.Get("content"))
			msgIdx++
			return true
		})
	}
	return out
}

// restoreSignaturesInContentArray 遍历 content 数组，仅对 type=="thinking" 且原始
// signature 非空的块，在 redacted 的同一路径写回真实 signature。
func restoreSignaturesInContentArray(redacted, prefix string, content gjson.Result) string {
	if !content.IsArray() {
		return redacted
	}
	out := redacted
	idx := 0
	content.ForEach(func(_, block gjson.Result) bool {
		i := idx
		idx++
		if block.Get("type").String() != "thinking" {
			return true
		}
		sig := block.Get("signature").String()
		if sig == "" {
			return true
		}
		path := prefix + "." + strconv.Itoa(i) + ".signature"
		if set, err := sjson.Set(out, path, sig); err == nil {
			out = set
		}
		return true
	})
	return out
}

// restoreThinkingSignatureInChunk 针对单个 SSE chunk：若原始 chunk 是 thinking 的
// signature_delta 事件，则把脱敏掉的 signature 值回填。Anthropic SSE 中 `signature`
// 只出现在 thinking 的 signature_delta，因此在 Anthropic+opt-in 门禁下是结构化安全的。
func restoreThinkingSignatureInChunk(redacted string, original []byte) string {
	sig := extractSignatureDelta(original)
	if sig == "" {
		return redacted
	}
	// signature_delta chunk 里 signature 恰好出现一次，定向替换被脱敏的占位。
	return strings.Replace(redacted, `"signature":"***"`, `"signature":`+strconv.Quote(sig), 1)
}

// extractSignatureDelta 从 SSE chunk 的 data 行解析 delta.signature（仅当 delta.type
// 为 signature_delta 时返回）。
func extractSignatureDelta(chunk []byte) string {
	for _, line := range strings.Split(string(chunk), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if !gjson.Valid(data) {
			continue
		}
		delta := gjson.Get(data, "delta")
		if delta.Get("type").String() == "signature_delta" {
			if s := delta.Get("signature").String(); s != "" {
				return s
			}
		}
	}
	return ""
}
