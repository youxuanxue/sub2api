package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTKCleanToolSchema_StripsDraft2020Keywords 钉住 2026-05-06 prod 事故：
// Anthropic→Gemini 桥接如果不剥掉 propertyNames / const / exclusiveMinimum /
// exclusiveMaximum，Google 上游会直接 400 "Invalid JSON payload received.
// Unknown name ...: Cannot find field."。同时验证 upstream cleanToolSchema
// 的既有清洗（type 大写化）以及 minimum / maximum / required 等被 Gemini
// 接受的字段不被误删。
func TestTKCleanToolSchema_StripsDraft2020Keywords(t *testing.T) {
	in := map[string]any{
		"type":     "object",
		"required": []any{"name"},
		"properties": map[string]any{
			"name": map[string]any{
				"type":  "string",
				"const": "auto",
			},
			"limit": map[string]any{
				"type":             "integer",
				"minimum":          1,
				"exclusiveMinimum": 0,
				"exclusiveMaximum": 100,
			},
			"tags": map[string]any{
				"type":          "object",
				"propertyNames": map[string]any{"pattern": "^[a-z]+$"},
			},
		},
	}

	cleaned, err := tkCleanToolSchema(in)
	require.NoError(t, err)
	out, ok := cleaned.(map[string]any)
	require.True(t, ok)

	props, ok := out["properties"].(map[string]any)
	require.True(t, ok)

	name, _ := props["name"].(map[string]any)
	require.NotContains(t, name, "const", "const must be rewritten away")
	require.Equal(t, []any{"auto"}, name["enum"], "string const must become a single-value enum")
	require.Equal(t, "STRING", name["type"], "upstream cleanToolSchema 的 type 大写化必须仍然生效")

	limit, _ := props["limit"].(map[string]any)
	require.NotContains(t, limit, "exclusiveMinimum", "exclusiveMinimum must be stripped")
	require.NotContains(t, limit, "exclusiveMaximum", "exclusiveMaximum must be stripped")
	require.Equal(t, 1, limit["minimum"], "supported keywords must survive cleaning")

	tags, _ := props["tags"].(map[string]any)
	require.NotContains(t, tags, "propertyNames", "propertyNames must be stripped")

	require.Contains(t, out, "required", "required 必须保留")
}

func TestCleanToolSchema_StringConstBecomesEnumAndAnnotationsStay(t *testing.T) {
	in := map[string]any{
		"type":     "object",
		"examples": []any{"root example"},
		"properties": map[string]any{
			"examples": map[string]any{"type": "string"},
			"mode": map[string]any{
				"type":  "string",
				"const": "browser",
				"enum":  []any{"browser", "shell"},
			},
			"count": map[string]any{
				"const": float64(1),
				"type":  "integer",
			},
			"note": map[string]any{
				"type": "string",
				"default": map[string]any{
					"pattern": "(?=keep)",
				},
			},
		},
	}

	out, ok := cleanToolSchema(in).(map[string]any)
	require.True(t, ok)
	require.NotContains(t, out, "examples")

	props, ok := out["properties"].(map[string]any)
	require.True(t, ok)
	require.Contains(t, props, "examples")
	mode, ok := props["mode"].(map[string]any)
	require.True(t, ok)
	require.NotContains(t, mode, "const")
	require.Equal(t, []any{"browser"}, mode["enum"])
	count, ok := props["count"].(map[string]any)
	require.True(t, ok)
	require.NotContains(t, count, "const")
	require.NotContains(t, count, "enum")
	require.Equal(t, "INTEGER", count["type"])
	note, ok := props["note"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, map[string]any{"pattern": "(?=keep)"}, note["default"])
}

// TestTKStripUnsupportedToolSchemaKeywords_RecursesIntoArrays 验证 TK strip
// 在 []any 容器（例如 anyOf / enum 数组、嵌套 properties 数组化场景）里也能
// 递归剥离。upstream cleanToolSchema 同样支持这条路径，TK strip 必须对齐。
func TestTKStripUnsupportedToolSchemaKeywords_RecursesIntoArrays(t *testing.T) {
	in := map[string]any{
		"anyOf": []any{
			map[string]any{
				"type":  "string",
				"const": "x",
			},
			map[string]any{
				"type":             "integer",
				"exclusiveMinimum": 0,
			},
		},
	}

	out, ok := tkStripUnsupportedToolSchemaKeywords(in).(map[string]any)
	require.True(t, ok)

	anyOf, ok := out["anyOf"].([]any)
	require.True(t, ok)
	require.Len(t, anyOf, 2)

	first, _ := anyOf[0].(map[string]any)
	require.Equal(t, "x", first["const"], "const 留给 cleanToolSchema 收成 enum，strip 不能先删")
	require.Equal(t, "string", first["type"], "TK strip 不做大写化（留给 upstream cleanToolSchema）")

	second, _ := anyOf[1].(map[string]any)
	require.NotContains(t, second, "exclusiveMinimum")
}
