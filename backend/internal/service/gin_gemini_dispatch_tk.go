package service

import "github.com/gin-gonic/gin"

// TKGeminiDispatchGroupContextKey 让 handler 把当前请求的 *Group 透传到
// gemini 桥接服务，而不需要修改 upstream Forward() 函数签名。
//
// Why gin.Context (而非加 Forward 形参)：
//   - upstream Forward(ctx, c, account, body) 已在多处被调用；改签名会扩
//     大 §5 upstream conflict surface，并迫使所有 caller 同步更新。
//   - 既有先例：BridgeGinAuthContextKey (gin_bridge_auth.go) 同样用
//     gin.Context 透传 TK 专属信息，避免污染 upstream 服务接口。
//
// 调用约定：handler 在调 (s *GeminiMessagesCompatService).Forward 之前
// 必须 c.Set(TKGeminiDispatchGroupContextKey, apiKey.Group)。Forward 体内
// 通过 tkGroupFromGinContext(c) 拿到 *Group（拿不到则 nil，resolver 会
// 自动 no-op）。
const TKGeminiDispatchGroupContextKey = "tk_gemini_dispatch_group"

// tkGroupFromGinContext 从 gin.Context 拉出 handler 提前 c.Set 的 *Group。
// 拿不到 / 类型不对 / nil context 一律返回 nil（caller 端做 nil-safe
// 处理 —— TKResolveGeminiDispatchModel 自身也接受 nil receiver）。
func tkGroupFromGinContext(c *gin.Context) *Group {
	if c == nil {
		return nil
	}
	v, ok := c.Get(TKGeminiDispatchGroupContextKey)
	if !ok {
		return nil
	}
	g, ok := v.(*Group)
	if !ok {
		return nil
	}
	return g
}
