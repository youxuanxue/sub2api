package service

import infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"

// newapi 账号 model_mapping 不变量(非对称)。
//
// newapi 是**多 vendor** 平台(一个平台下 deepseek / Qwen / volcengine / google-vertex /
// grok / … 多个 vendor 组,按 channel_type 区分)。空 model_mapping 在多 vendor 平台是配置
// 缺失:全能 Key 解析器无法据此判别该账号到底服务哪些模型,会按平台名误路由(grok 账号 65
// 无声明即栽于此)。故强制 newapi 账号声明非空 model_mapping。
//
// 原生单 vendor 平台(anthropic / openai / gemini / antigravity)保留「空映射=透传该平台
// 全部模型」——零维护、忠于上游,不受此约束。
//
// 三道闸之一(写时挡)。direct-scheduler parity provider 会忠于 direct key 的账号级语义,
// 因此写时校验是 newapi 空映射不进入生产路由的主闸；GetAvailableModels fallback 仍对
// newapi 空映射组不匹配(universal_routing_tk_serving.go),ops 实时审计继续兜底
// (ops/newapi/audit-model-mapping.py)。见 docs/approved/universal-key-routing.md。

// ErrNewapiModelMappingRequired 是 newapi 账号缺/空 model_mapping 时的写时校验错误。
var ErrNewapiModelMappingRequired = infraerrors.BadRequest(
	"NEWAPI_MODEL_MAPPING_REQUIRED",
	"newapi 账号必须声明非空 credentials.model_mapping(多 vendor 平台空映射会导致全能 Key 误路由)",
)

// validateNewapiAccountModelMapping 仅对 platform==newapi 强制非空 model_mapping。
func validateNewapiAccountModelMapping(platform string, credentials map[string]any) error {
	if platform != PlatformNewAPI {
		return nil
	}
	if !newapiModelMappingDeclared(credentials) {
		return ErrNewapiModelMappingRequired
	}
	return nil
}

// newapiModelMappingDeclared 判定 credentials 是否声明了非空 model_mapping(原始声明,不走
// GetModelMapping 的平台默认回退——要的是「显式声明」而非「被默认填充」)。
func newapiModelMappingDeclared(credentials map[string]any) bool {
	raw, ok := credentials["model_mapping"]
	if !ok {
		return false
	}
	switch m := raw.(type) {
	case map[string]any:
		return len(m) > 0
	case map[string]string:
		return len(m) > 0
	default:
		return false
	}
}
