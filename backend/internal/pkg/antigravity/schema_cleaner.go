package antigravity

import (
	"fmt"
	"strings"
)

// CleanJSONSchema 清理 JSON Schema，移除 Antigravity/Gemini 不支持的字段
// 参考 Antigravity-Manager/src-tauri/src/proxy/common/json_schema.rs 实现
// 确保 schema 符合 JSON Schema draft 2020-12 且适配 Gemini v1internal
func CleanJSONSchema(schema map[string]any) (map[string]any, error) {
	return cleanJSONSchema(schema, true)
}

func cleanJSONSchema(schema map[string]any, toolRoot bool) (map[string]any, error) {
	if schema == nil {
		return nil, nil
	}
	// Expand into a bounded copy before any recursive cleanup. A recursive JSON
	// Schema is valid JSON but cannot be represented by Gemini's inline schema.
	if len(schema) > maxSchemaExpansionNodes {
		return nil, schemaExpansionLimitError()
	}
	for _, key := range []string{"$defs", "definitions"} {
		if defs, ok := schema[key].(map[string]any); ok && len(defs) > maxSchemaExpansionNodes {
			return nil, schemaExpansionLimitError()
		}
	}
	root := make(map[string]any, len(schema))
	for key, value := range schema {
		root[key] = value
	}
	expander := schemaReferenceExpander{defs: extractDefs(root), active: make(map[string]bool)}
	expanded, err := expander.expand(root, 0, nil)
	if err != nil {
		return nil, err
	}
	DeepCleanUndefined(expanded)
	cleaned := cleanJSONSchemaNode(expanded, toolRoot, toolRoot)
	result, _ := cleaned.(map[string]any)
	return result, nil
}

// SchemaExpansionError describes input that cannot safely be flattened. It is
// a client request error, never a reason to forward an uncleaned schema.
type SchemaExpansionError struct{ reason string }

func (e *SchemaExpansionError) Error() string { return "unsupported JSON schema: " + e.reason }

const (
	maxSchemaExpansionDepth = 128
	maxSchemaExpansionNodes = 32768
	maxSchemaExpansionBytes = 8 << 20
)

type schemaReferenceExpander struct {
	defs   map[string]any
	active map[string]bool
	nodes  int
	bytes  int
}

func schemaExpansionLimitError() error {
	return &SchemaExpansionError{reason: "reference expansion exceeds the depth or size limit"}
}

// reserveString uses JSON's worst case of six bytes per input byte for escaping.
// Bound before multiplying so hostile lengths cannot overflow the counter.
func (e *schemaReferenceExpander) reserveString(value string) error {
	if len(value) > (maxSchemaExpansionBytes-e.bytes-2)/6 {
		return schemaExpansionLimitError()
	}
	e.bytes += 2 + 6*len(value)
	return nil
}

func (e *schemaReferenceExpander) expand(value any, depth int, omitted map[string]bool) (any, error) {
	e.nodes++
	e.bytes += 32 // delimiters and scalar values; strings/keys have their own budget
	if depth > maxSchemaExpansionDepth || e.nodes > maxSchemaExpansionNodes || e.bytes > maxSchemaExpansionBytes {
		return nil, schemaExpansionLimitError()
	}
	switch node := value.(type) {
	case map[string]any:
		if len(node) > maxSchemaExpansionNodes-e.nodes {
			return nil, schemaExpansionLimitError()
		}
		if ref, ok := node["$ref"].(string); ok && len(e.defs) > 0 {
			parts := strings.Split(ref, "/")
			name := parts[len(parts)-1]
			// Siblings are independent input, not part of this reference chain. Expand
			// them before entering the definition and omit their keys throughout alias
			// resolution, so overridden recursive branches are never traversed.
			siblings := make(map[string]any, len(node))
			blocked := make(map[string]bool, len(omitted)+len(node))
			for key := range omitted {
				blocked[key] = true
			}
			for key, child := range node {
				if key != "$ref" && !omitted[key] {
					siblings[key] = child
					blocked[key] = true
				}
			}
			own, err := e.expand(siblings, depth+1, nil)
			if err != nil {
				return nil, err
			}
			result, _ := own.(map[string]any)
			if definition, ok := e.defs[name].(map[string]any); ok {
				if e.active[name] {
					return nil, &SchemaExpansionError{reason: "recursive references are not supported"}
				}
				e.active[name] = true
				inherited, err := e.expand(definition, depth+1, blocked)
				delete(e.active, name)
				if err != nil {
					return nil, err
				}
				inheritedMap, _ := inherited.(map[string]any)
				for key, child := range inheritedMap {
					result[key] = child
				}
			}
			return result, nil
		}
		result := make(map[string]any, len(node))
		for key, child := range node {
			if omitted[key] {
				continue
			}
			if err := e.reserveString(key); err != nil {
				return nil, err
			}
			copied, err := e.expand(child, depth+1, nil)
			if err != nil {
				return nil, err
			}
			result[key] = copied
		}
		return result, nil
	case []any:
		if len(node) > maxSchemaExpansionNodes-e.nodes {
			return nil, schemaExpansionLimitError()
		}
		result := make([]any, len(node))
		for i, child := range node {
			copied, err := e.expand(child, depth+1, nil)
			if err != nil {
				return nil, err
			}
			result[i] = copied
		}
		return result, nil
	case string:
		if err := e.reserveString(node); err != nil {
			return nil, err
		}
		return node, nil
	default:
		return value, nil
	}
}

// extractDefs 提取并移除定义的 helper
func extractDefs(schema map[string]any) map[string]any {
	defs := make(map[string]any)
	if d, ok := schema["$defs"].(map[string]any); ok {
		for k, v := range d {
			defs[k] = v
		}
		delete(schema, "$defs")
	}
	if d, ok := schema["definitions"].(map[string]any); ok {
		for k, v := range d {
			defs[k] = v
		}
		delete(schema, "definitions")
	}
	return defs
}

// deepCopy 深拷贝 (简单实现，仅针对 JSON 类型)
func deepCopy(src any) any {
	if src == nil {
		return nil
	}
	switch v := src.(type) {
	case map[string]any:
		dst := make(map[string]any)
		for k, val := range v {
			dst[k] = deepCopy(val)
		}
		return dst
	case []any:
		dst := make([]any, len(v))
		for i, val := range v {
			dst[i] = deepCopy(val)
		}
		return dst
	default:
		return src
	}
}

// cleanJSONSchemaNode shares schema normalization while limiting invented empty
// object placeholders to tool parameters; response schemas retain their shape.
func cleanJSONSchemaNode(value any, isRoot, toolSchema bool) any {
	schemaMap, ok := value.(map[string]any)
	if !ok {
		return value
	}

	// 0. [NEW] 合并 allOf
	mergeAllOf(schemaMap)

	// 0.5 [FIX] 处理 Draft 2020-12 的 prefixItems (元组定义)
	if prefixItemsVal, hasPrefix := schemaMap["prefixItems"]; hasPrefix {
		if prefixItems, ok := prefixItemsVal.([]any); ok && len(prefixItems) > 0 {
			itemsVal, hasItems := schemaMap["items"]
			_, itemsIsBool := itemsVal.(bool)
			// 如果没有 items，或者 items 为 nil，或者 items 是布尔值（如 Draft 2020-12 中表示元组闭合的 items: false），
			// 用选出的最佳元组项填充 items，以满足 Gemini 对 array.items 必须为合法 Schema 对象的规范。
			if !hasItems || itemsVal == nil || itemsIsBool {
				best := extractBestSchemaFromUnion(prefixItems)
				if best == nil {
					best = map[string]any{"type": "string"}
				}
				schemaMap["items"] = best
			}
		}
		// 无论 prefixItems 是否为空数组，都彻底删除 prefixItems 关键字以防 Gemini 400
		delete(schemaMap, "prefixItems")
	}

	// 1. [CRITICAL] 深度递归处理子项
	if props, ok := schemaMap["properties"].(map[string]any); ok {
		for _, v := range props {
			cleanJSONSchemaNode(v, false, toolSchema)
		}
	}
	if items, ok := schemaMap["items"]; ok {
		// [FIX] Gemini 期望 "items" 是单个 Schema 对象（列表验证），而不是数组（元组验证）。
		if itemsArr, ok := items.([]any); ok {
			// Draft-07 元组形式 items: [A, B]。Gemini 只接受单个 items Schema
			// (列表校验)，位置约束无法表达；但不能像以前那样只保留最佳分支、
			// 把其余位置声明整段丢掉。合并所有位置的字段，再交给常规清洗。
			schemaMap["items"] = cleanJSONSchemaNode(mergeTupleEntries(itemsArr), false, toolSchema)
		} else {
			cleanJSONSchemaNode(items, false, toolSchema)
		}
	}
	// 如果既无 properties 也无 items，遍历其他 map/slice 子项递归
	if !hasKey(schemaMap, "properties") && !hasKey(schemaMap, "items") {
		for _, v := range schemaMap {
			if _, isMap := v.(map[string]any); isMap {
				cleanJSONSchemaNode(v, false, toolSchema)
			} else if arr, isArr := v.([]any); isArr {
				for _, item := range arr {
					cleanJSONSchemaNode(item, false, toolSchema)
				}
			}
		}
	}

	// 2. [FIX] 处理 anyOf/oneOf 联合类型: 合并属性而非直接删除
	// type:"array" 必须和 object / 无 type 一样参与合并：白名单过滤 (步骤 5) 会删掉
	// anyOf，分支里的 items 随之消失，再被步骤 8 的 array 兜底填成 items:{type:string}，
	// 把 array-of-object 降级成 array-of-string。
	var unionArray []any
	typeStr, _ := schemaMap["type"].(string)
	if typeStr == "" || typeStr == "object" || typeStr == "array" {
		if anyOf, ok := schemaMap["anyOf"].([]any); ok {
			unionArray = anyOf
		} else if oneOf, ok := schemaMap["oneOf"].([]any); ok {
			unionArray = oneOf
		}
	}

	if len(unionArray) > 0 {
		if bestBranch := extractBestSchemaFromUnion(unionArray); bestBranch != nil {
			if bestMap, ok := bestBranch.(map[string]any); ok {
				// 合并分支内容
				for k, v := range bestMap {
					if k == "properties" {
						targetProps, _ := schemaMap["properties"].(map[string]any)
						if targetProps == nil {
							targetProps = make(map[string]any)
							schemaMap["properties"] = targetProps
						}
						if sourceProps, ok := v.(map[string]any); ok {
							for pk, pv := range sourceProps {
								if _, exists := targetProps[pk]; !exists {
									targetProps[pk] = deepCopy(pv)
								}
							}
						}
					} else if k == "required" {
						targetReq, _ := schemaMap["required"].([]any)
						if sourceReq, ok := v.([]any); ok {
							for _, rv := range sourceReq {
								// 简单的去重添加
								exists := false
								for _, tr := range targetReq {
									if tr == rv {
										exists = true
										break
									}
								}
								if !exists {
									targetReq = append(targetReq, rv)
								}
							}
							schemaMap["required"] = targetReq
						}
					} else if _, exists := schemaMap[k]; !exists {
						schemaMap[k] = deepCopy(v)
					}
				}
				// 清除已合并的 union 关键字
				delete(schemaMap, "anyOf")
				delete(schemaMap, "oneOf")
				// 对合并进来的新属性和 items 进行深度递归清洗，确保合并进来的子结构（如 prefixItems）也能被处理
				if props, ok := schemaMap["properties"].(map[string]any); ok {
					for _, pv := range props {
						cleanJSONSchemaNode(pv, false, toolSchema)
					}
				}
				if items, ok := schemaMap["items"]; ok {
					cleanJSONSchemaNode(items, false, toolSchema)
				}
			}
		}
	}

	// 3. [SAFETY] 检查当前对象是否为 JSON Schema 节点
	looksLikeSchema := hasKey(schemaMap, "type") ||
		hasKey(schemaMap, "properties") ||
		hasKey(schemaMap, "items") ||
		hasKey(schemaMap, "enum") ||
		hasKey(schemaMap, "const") ||
		hasKey(schemaMap, "anyOf") ||
		hasKey(schemaMap, "oneOf") ||
		hasKey(schemaMap, "allOf")

	if looksLikeSchema {
		// 4. [ROBUST] 约束迁移与 const 规范化
		migrateConstraints(schemaMap)

		// 规范化 const 关键字 (Draft 6+ 转换为 Gemini 兼容的 enum: [const])
		if constVal, exists := schemaMap["const"]; exists {
			if _, hasEnum := schemaMap["enum"]; !hasEnum {
				schemaMap["enum"] = []any{constVal}
			} else if constant, ok := constVal.(string); ok {
				if existing, ok := schemaMap["enum"].([]any); ok {
					// Both constraints apply; preserve their intersection before dropping const.
					values := []any{}
					for _, value := range existing {
						if text, ok := value.(string); ok && text == constant {
							values = append(values, constant)
							break
						}
					}
					schemaMap["enum"] = values
				}
			}
			if _, hasType := schemaMap["type"]; !hasType {
				switch constVal.(type) {
				case string:
					schemaMap["type"] = "string"
				case int, int32, int64:
					schemaMap["type"] = "integer"
				case float32, float64:
					schemaMap["type"] = "number"
				case bool:
					schemaMap["type"] = "boolean"
				default:
					schemaMap["type"] = "string"
				}
			}
			delete(schemaMap, "const")
		}

		// 5. [CRITICAL] 白名单过滤
		allowedFields := map[string]bool{
			"type":        true,
			"description": true,
			"properties":  true,
			"required":    true,
			"items":       true,
			"enum":        true,
			"title":       true,
		}
		for k := range schemaMap {
			if !allowedFields[k] {
				delete(schemaMap, k)
			}
		}

		// 6. 处理 type 字段 (Lowercase + Nullable 提取)
		isEffectivelyNullable := false
		if typeVal, exists := schemaMap["type"]; exists {
			var selectedType string
			switch v := typeVal.(type) {
			case string:
				lower := strings.ToLower(v)
				if lower == "null" {
					isEffectivelyNullable = true
					selectedType = "string" // fallback
				} else {
					selectedType = lower
				}
			case []any:
				// ["string", "null"]
				for _, t := range v {
					if ts, ok := t.(string); ok {
						lower := strings.ToLower(ts)
						if lower == "null" {
							isEffectivelyNullable = true
						} else if selectedType == "" {
							selectedType = lower
						}
					}
				}
				if selectedType == "" {
					selectedType = "string"
				}
			}
			schemaMap["type"] = selectedType
		} else {
			// 如果没有 type，但有 properties/items/enum，推断并补全
			if hasKey(schemaMap, "properties") {
				schemaMap["type"] = "object"
			} else if hasKey(schemaMap, "items") {
				schemaMap["type"] = "array"
			} else if enumArr, ok := schemaMap["enum"].([]any); ok && len(enumArr) > 0 {
				// [FIX] 针对仅含 enum 的 schema，按其元素类型推断标量类型，避免被误判为 object 并注入 reason 属性
				switch enumArr[0].(type) {
				case int, int32, int64:
					schemaMap["type"] = "integer"
				case float32, float64:
					schemaMap["type"] = "number"
				case bool:
					schemaMap["type"] = "boolean"
				default:
					schemaMap["type"] = "string"
				}
			} else if hasKey(schemaMap, "enum") {
				schemaMap["type"] = "string"
			} else {
				schemaMap["type"] = "object"
			}
		}

		// 7. [SAFETY] 处理空 Object
		if t, _ := schemaMap["type"].(string); t == "object" {
			hasProps := false
			if props, ok := schemaMap["properties"].(map[string]any); ok && len(props) > 0 {
				hasProps = true
			}
			if !hasProps && toolSchema {
				if isRoot {
					// 工具参数根节点确实没有参数：保留 upstream 的 reason 占位，
					// 这是「无参工具」的既有契约。
					schemaMap["properties"] = map[string]any{
						"reason": map[string]any{
							"type":        "string",
							"description": "Reason for calling this tool",
						},
					}
					schemaMap["required"] = []any{"reason"}
				} else {
					// 嵌套 object 为空通常不是「无字段」，而是 additionalProperties /
					// patternProperties 形式的字符串映射被白名单过滤掉了。以前这里
					// 复用根节点那段，凭空注入 required 的 "reason"：客户端从未声明
					// 该字段，模型却会照着填。Gemini 要求 object 必须带 properties，
					// 所以仍给一个占位，但 **不进 required**，模型可以整体省略。
					schemaMap["properties"] = map[string]any{
						placeholderObjectPropertyName: map[string]any{
							"type":        "string",
							"description": "Free-form entry; this object accepts arbitrary string-valued keys",
						},
					}
					delete(schemaMap, "required")
				}
			}
		}

		// 8. [SAFETY] 处理缺失 items 的 Array (Gemini 严苛要求 array 必须声明 items Schema)
		if t, _ := schemaMap["type"].(string); t == "array" {
			itemsVal, hasItems := schemaMap["items"]
			needDefaultItems := false
			if !hasItems || itemsVal == nil {
				needDefaultItems = true
			} else if itemsMap, ok := itemsVal.(map[string]any); ok {
				if len(itemsMap) == 0 {
					needDefaultItems = true
				}
			} else if _, isBool := itemsVal.(bool); isBool {
				needDefaultItems = true
			}
			if needDefaultItems {
				schemaMap["items"] = map[string]any{
					"type": "string",
				}
			}
		}

		// 9. [SAFETY] Required 字段对齐
		if props, ok := schemaMap["properties"].(map[string]any); ok {
			if req, ok := schemaMap["required"].([]any); ok {
				var validReq []any
				for _, r := range req {
					if rStr, ok := r.(string); ok {
						if _, exists := props[rStr]; exists {
							validReq = append(validReq, r)
						}
					}
				}
				if len(validReq) > 0 {
					schemaMap["required"] = validReq
				} else {
					delete(schemaMap, "required")
				}
			}
		}

		if isEffectivelyNullable {
			desc, _ := schemaMap["description"].(string)
			if !strings.Contains(desc, "nullable") {
				if desc != "" {
					desc += " "
				}
				desc += "(nullable)"
				schemaMap["description"] = desc
			}
		}

		// 9. Enum 值强制转字符串
		if enumVals, ok := schemaMap["enum"].([]any); ok {
			hasNonString := false
			for i, val := range enumVals {
				if _, isStr := val.(string); !isStr {
					hasNonString = true
					if val == nil {
						enumVals[i] = "null"
					} else {
						enumVals[i] = fmt.Sprintf("%v", val)
					}
				}
			}
			// If we mandated string values, we must ensure type is string
			if hasNonString {
				schemaMap["type"] = "string"
			}
		}
	}

	return schemaMap
}

// placeholderObjectPropertyName 是嵌套空 object 的占位字段名。它只为满足
// Gemini「object 必须声明 properties」而存在，永不进 required。
const placeholderObjectPropertyName = "value"

func hasKey(m map[string]any, k string) bool {
	_, ok := m[k]
	return ok
}

func migrateConstraints(m map[string]any) {
	constraints := []struct {
		key   string
		label string
	}{
		{"minLength", "minLen"},
		{"maxLength", "maxLen"},
		{"pattern", "pattern"},
		{"minimum", "min"},
		{"maximum", "max"},
		{"multipleOf", "multipleOf"},
		{"exclusiveMinimum", "exclMin"},
		{"exclusiveMaximum", "exclMax"},
		{"minItems", "minItems"},
		{"maxItems", "maxItems"},
		{"propertyNames", "propertyNames"},
		{"format", "format"},
	}

	var hints []string
	for _, c := range constraints {
		if val, ok := m[c.key]; ok && val != nil {
			hints = append(hints, fmt.Sprintf("%s: %v", c.label, val))
		}
	}

	if len(hints) > 0 {
		suffix := fmt.Sprintf(" [Constraint: %s]", strings.Join(hints, ", "))
		desc, _ := m["description"].(string)
		if !strings.Contains(desc, suffix) {
			m["description"] = desc + suffix
		}
	}
}

// mergeAllOf 合并 allOf
func mergeAllOf(m map[string]any) {
	allOf, ok := m["allOf"].([]any)
	if !ok {
		return
	}
	delete(m, "allOf")

	mergedProps := make(map[string]any)
	mergedReq := make(map[string]bool)
	otherFields := make(map[string]any)

	for _, sub := range allOf {
		if subMap, ok := sub.(map[string]any); ok {
			// Props
			if props, ok := subMap["properties"].(map[string]any); ok {
				for k, v := range props {
					mergedProps[k] = v
				}
			}
			// Required
			if reqs, ok := subMap["required"].([]any); ok {
				for _, r := range reqs {
					if s, ok := r.(string); ok {
						mergedReq[s] = true
					}
				}
			}
			// Others
			for k, v := range subMap {
				if k != "properties" && k != "required" && k != "allOf" {
					if _, exists := otherFields[k]; !exists {
						otherFields[k] = v
					}
				}
			}
		}
	}

	// Apply
	for k, v := range otherFields {
		if _, exists := m[k]; !exists {
			m[k] = v
		}
	}
	if len(mergedProps) > 0 {
		existProps, _ := m["properties"].(map[string]any)
		if existProps == nil {
			existProps = make(map[string]any)
			m["properties"] = existProps
		}
		for k, v := range mergedProps {
			if _, exists := existProps[k]; !exists {
				existProps[k] = v
			}
		}
	}
	if len(mergedReq) > 0 {
		existReq, _ := m["required"].([]any)
		var validReqs []any
		for _, r := range existReq {
			if s, ok := r.(string); ok {
				validReqs = append(validReqs, s)
				delete(mergedReq, s) // already exists
			}
		}
		// append new
		for r := range mergedReq {
			validReqs = append(validReqs, r)
		}
		m["required"] = validReqs
	}
}

// mergeTupleEntries 把 Draft-07 元组 items: [A, B] 压成单个列表校验 Schema。
// 以最佳分支为底，再把其余位置独有的 properties / enum 并进来，保证没有任何
// 已声明字段消失。required 被刻意丢弃：某个位置的必填项对其他位置不成立，
// 保留它会让模型以为每个元素都必须带该字段。
func mergeTupleEntries(entries []any) any {
	best, _ := deepCopy(extractBestSchemaFromUnion(entries)).(map[string]any)
	if best == nil {
		return map[string]any{"type": "string"}
	}
	delete(best, "required")

	for _, entry := range entries {
		entryMap, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if props, ok := entryMap["properties"].(map[string]any); ok {
			target, _ := best["properties"].(map[string]any)
			if target == nil {
				target = make(map[string]any)
				best["properties"] = target
			}
			for k, v := range props {
				if _, exists := target[k]; !exists {
					target[k] = deepCopy(v)
				}
			}
		}
		if enum, ok := entryMap["enum"].([]any); ok {
			target, _ := best["enum"].([]any)
			for _, v := range enum {
				duplicate := false
				for _, existing := range target {
					if existing == v {
						duplicate = true
						break
					}
				}
				if !duplicate {
					target = append(target, deepCopy(v))
				}
			}
			best["enum"] = target
		}
	}
	return best
}

// extractBestSchemaFromUnion 从 anyOf/oneOf 中选取最佳分支
func extractBestSchemaFromUnion(unionArray []any) any {
	var bestOption any
	bestScore := -1

	for _, item := range unionArray {
		score := scoreSchemaOption(item)
		if score > bestScore {
			bestScore = score
			bestOption = item
		}
	}
	return bestOption
}

func scoreSchemaOption(val any) int {
	m, ok := val.(map[string]any)
	if !ok {
		return 0
	}
	typeStr, _ := m["type"].(string)

	if hasKey(m, "properties") || typeStr == "object" {
		return 3
	}
	if hasKey(m, "items") || typeStr == "array" {
		return 2
	}
	if hasKey(m, "enum") || hasKey(m, "const") {
		return 1
	}
	if typeStr != "" && typeStr != "null" {
		return 1
	}
	return 0
}

// DeepCleanUndefined 深度清理值为 "[undefined]" 的字段，返回删除的键数量。
func DeepCleanUndefined(value any) int {
	if value == nil {
		return 0
	}
	removed := 0
	switch v := value.(type) {
	case map[string]any:
		for k, val := range v {
			if s, ok := val.(string); ok && s == "[undefined]" {
				delete(v, k)
				removed++
				continue
			}
			removed += DeepCleanUndefined(val)
		}
	case []any:
		for _, val := range v {
			removed += DeepCleanUndefined(val)
		}
	}
	return removed
}
