package service

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// tkApplyCodexOAuthForwardBody applies Codex OAuth body transforms and stages
// fingerprint IDs for the non-passthrough Forward path. Caller must ensureReqBody
// before invoke and markDecodedModified when modified is true.
func (s *OpenAIGatewayService) tkApplyCodexOAuthForwardBody(
	c *gin.Context,
	account *Account,
	decoded map[string]any,
	isCodexCLI, isCompactRequest, compatMessagesBridge bool,
	upstreamModel, promptCacheKey string,
) (newUpstreamModel, newPromptCacheKey string, modified bool, err error) {
	newUpstreamModel = upstreamModel
	newPromptCacheKey = promptCacheKey

	codexResult := codexTransformResult{}
	if compatMessagesBridge {
		codexResult = applyCodexOAuthTransformWithOptions(decoded, codexOAuthTransformOptions{IsCodexCLI: isCodexCLI, IsCompact: isCompactRequest, SkipDefaultInstructions: true, PreserveToolCallIDs: true})
		ensureCodexOAuthInstructionsField(decoded)
		modified = true
	} else {
		codexResult = applyCodexOAuthTransform(decoded, isCodexCLI, isCompactRequest)
	}
	if codexResult.Error != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request_error", "message": codexResult.Error.Error()}})
		return newUpstreamModel, newPromptCacheKey, modified, codexResult.Error
	}
	setCodexToolNameReverse(c, codexResult.ToolNameReverse)
	if codexResult.Modified {
		modified = true
	}
	// 带真实 device_id 时补齐 client_metadata 安装标识，与真实 Codex 对齐（compact 形态不同，跳过）。
	if !isCompactRequest && applyCodexClientMetadata(decoded, account) {
		modified = true
	}
	stageCodexFingerprintIDs(c, nil)
	// 指纹收敛：一次性解析收敛 ID，请求体和出站头共享同一份 IDs（保证 turn_id 等随机字段一致）。
	// fingerprintIDs 在此处解析，后续 buildUpstreamRequest 中使用同一份。
	if !isCompactRequest {
		var clientHeaders http.Header
		if c != nil && c.Request != nil {
			clientHeaders = c.Request.Header
		}
		fpIDs := resolveCodexFingerprintIDsFromRequest(account, clientHeaders)
		if fpIDs != nil {
			if applyCodexFingerprintClientMetadata(decoded, fpIDs) {
				modified = true
			}
		}
		// 将 fpIDs 存入 gin context，供 buildUpstreamRequest 中头改写使用。
		// 无条件覆写（含 nil）：failover 从收敛账号切到 off 账号时，上一
		// 账号的 IDs 不得残留（stageCodexFingerprintIDs 注释）。
		stageCodexFingerprintIDs(c, fpIDs)
	}
	if codexResult.NormalizedModel != "" {
		newUpstreamModel = codexResult.NormalizedModel
	}
	if currentPromptCacheKey, ok := decoded["prompt_cache_key"].(string); ok && currentPromptCacheKey != "" {
		newPromptCacheKey = currentPromptCacheKey
	} else if codexResult.PromptCacheKey != "" {
		newPromptCacheKey = codexResult.PromptCacheKey
	}
	return newUpstreamModel, newPromptCacheKey, modified, nil
}
