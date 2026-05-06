package service

// TK companion to upstream cleanToolSchema in gemini_messages_compat_service.go.
//
// 为什么独立成文件：
// upstream cleanToolSchema 自己维护了一份 Gemini 不识别的 JSON Schema 字段
// denylist (90b38381 加过 patternProperties)，函数体处于 upstream 持续演进
// 路径上。直接在那个 if-key 链中加 TK-only 字段会在每次 upstream 调整 denylist
// 时撞 textual conflict，并且最坏情形下被 git merge 的 upstream-favor 自动解
// 决悄悄回退（违反 §5 "minimum upstream conflict surface" 与 §5.x "no silent
// upstream override"）。所以 TK 这边把额外字段集中在本 companion，upstream
// 文件只需要 1-token 的 call-site 切换 (cleanToolSchema → tkCleanToolSchema)。
//
// 历史背景：
// 2026-05-06 prod 事故：claude-code 通过 /v1/messages → gemini-pa group 发
// gemini-3.1-pro-preview，Google 上游 400 拒掉 tool schema：
//   Invalid JSON payload received. Unknown name "propertyNames" / "const" /
//   "exclusiveMinimum" at request.tools[0].function_declarations[*].parameters
//   .properties[*].value: Cannot find field.
// 这些都是 JSON Schema Draft 2020 / OpenAPI 3.1 引入的关键字，不在 Gemini
// OpenAPI 3.0 受限子集 (ai.google.dev/api/caching#Schema) 内。
//
// 何时拆掉本文件：
// 如果 upstream 哪天独立把这些字段加进 cleanToolSchema 的 denylist，本文件
// 的 strip 就成了幂等冗余，可以删掉本文件并把 call-site 还原回
// cleanToolSchema —— 这是单 PR 即可完成的 mechanical revert。

// tkUnsupportedToolSchemaKeywords 是 upstream cleanToolSchema 漏掉、但
// Gemini 一定 400 的 JSON Schema 字段集。任何加入此表的字段都必须在 PR
// 描述里附 prod 错误日志或 Gemini 文档引用，避免无证据的 deny 蔓延。
var tkUnsupportedToolSchemaKeywords = map[string]struct{}{
	"propertyNames":    {}, // Draft 6+: properties name schema
	"const":            {}, // Draft 6+: literal constraint
	"exclusiveMinimum": {}, // Draft 6+ numeric form
	"exclusiveMaximum": {}, // Draft 6+ numeric form
}

// tkCleanToolSchema 是 upstream cleanToolSchema 的 TK extended 包装：先递归
// 剥掉 TK 维护的额外不兼容关键字，再交给 upstream cleanToolSchema 完成既
// 有清洗 (大写化 type、删 $schema 等)。call-site 见
// convertClaudeToolsToGeminiTools 中的 cleanedParams 赋值。
func tkCleanToolSchema(schema any) any {
	return cleanToolSchema(tkStripUnsupportedToolSchemaKeywords(schema))
}

// tkStripUnsupportedToolSchemaKeywords 递归删掉 tkUnsupportedToolSchemaKeywords
// 列出的字段。结构与 cleanToolSchema 类似但只做 strip，不做大写化 (留给
// upstream cleanToolSchema 处理，避免 TK 这边重复实现 type 大写化语义)。
func tkStripUnsupportedToolSchemaKeywords(schema any) any {
	if schema == nil {
		return nil
	}
	switch v := schema.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, value := range v {
			if _, drop := tkUnsupportedToolSchemaKeywords[key]; drop {
				continue
			}
			out[key] = tkStripUnsupportedToolSchemaKeywords(value)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = tkStripUnsupportedToolSchemaKeywords(item)
		}
		return out
	default:
		return v
	}
}
