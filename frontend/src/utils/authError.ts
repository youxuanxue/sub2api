// 后端错误响应在 `client.ts` 的 axios 拦截器里被展平为
//   { status, code, reason, message, metadata }
// 其中 `reason` 是稳定的 SCREAMING_SNAKE_CASE 业务码（例如
// `TURNSTILE_VERIFICATION_FAILED`、`INVALID_CREDENTIALS`），适合用作 i18n key
// 的查找键；`message` / `detail` 是后端给的兜底文案。
//
// 历史上 axios 直出 `{ response: { data: { detail, message } } }` 形态的对象，
// 本工具同时兼容两种形态以平滑过渡，新代码请优先依赖 `reason`。
interface APIErrorLike {
  message?: string
  reason?: string
  response?: {
    data?: {
      detail?: string
      message?: string
      reason?: string
    }
  }
}

function extractReason(error: unknown): string {
  const err = (error || {}) as APIErrorLike
  return err.reason || err.response?.data?.reason || ''
}

function extractErrorMessage(error: unknown): string {
  const err = (error || {}) as APIErrorLike
  return err.response?.data?.detail || err.response?.data?.message || err.message || ''
}

export interface BuildAuthErrorMessageOptions {
  /**
   * 当其他来源都拿不到文案时使用的兜底文案。必填，避免出现空白错误条。
   */
  fallback: string
  /**
   * 把后端 `reason` 业务码映射成专用文案的覆盖表。
   *
   * 命中时**优先于** detail/message 使用——典型场景是「同一类失败，根据 reason
   * 给出可执行的自救建议」（例如 stale Turnstile token → "请刷新页面"）。
   * 后端文案对终端用户太抽象时，前端在此显式翻译。
   */
  reasonOverrides?: Record<string, string>
}

export function buildAuthErrorMessage(error: unknown, options: BuildAuthErrorMessageOptions): string {
  const { fallback, reasonOverrides } = options
  if (reasonOverrides) {
    const reason = extractReason(error)
    if (reason && reasonOverrides[reason]) {
      return reasonOverrides[reason]
    }
  }
  const message = extractErrorMessage(error)
  return message || fallback
}

export function unknownToErrorMessage(error: unknown, fallback = 'Unknown error'): string {
  return extractErrorMessage(error) || fallback
}
