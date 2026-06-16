import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'

// 第七平台 Grok (xAI / SuperGrok Heavy) 的添加 / 编辑 modal 业务状态 + 校验 +
// credentials 拼装都收口在本 composable，让 CreateAccountModal 只剩「调用 + 透传
// v-model」。见 CLAUDE.md §5.x「最小侵入 + composable 收口」。
//
// 账号 credentials 契约（后端已定）：platform=grok, type=oauth
//   - refresh_token  (必填) —— 由 xAI Grok CLI 本机 loopback 登录铸取后粘贴；
//                    xAI 公共 client 无 web-redirect / device-code，故服务端做不了
//                    交互式 OAuth。创建时后端 resolveGrokTokenOnSave 会用它立刻换
//                    access_token（绿勾 / 明确报错），无需手填 access_token。
//   - base_url       (可选) —— 默认 https://api.x.ai/v1，仅自建反代时覆盖。
//
// 比 kiro 简单得多：xAI 是 OpenAI-wire 兼容，只需 refresh_token 一项。

export const GROK_DEFAULT_BASE_URL = 'https://api.x.ai/v1'

export interface TkGrokCredentialsBundle {
  credentials: Record<string, unknown>
}

export interface TkGrokAccountSnapshot {
  credentials?: Record<string, unknown> | null
}

/**
 * Grok（第七平台）添加 / 编辑账号表单的状态与副作用。
 *
 * 暴露：
 *   - v-model 绑定的 ref（refreshToken / baseUrl）
 *   - 父组件调用的方法（reset / populateFromAccount / buildSubmitBundle）
 */
export function useTkAccountGrokPlatform() {
  const { t } = useI18n()
  const appStore = useAppStore()

  const refreshToken = ref('')
  const baseUrl = ref('')

  function reset(): void {
    refreshToken.value = ''
    baseUrl.value = ''
  }

  /**
   * 把已有 grok 账号字段镜像到 ref（EditAccountModal 用）。refresh_token 敏感，
   * 编辑时留空表示「保留现有值」，不回填。
   */
  function populateFromAccount(account: TkGrokAccountSnapshot): void {
    const credentials = (account.credentials || {}) as Record<string, unknown>
    refreshToken.value = ''
    baseUrl.value = typeof credentials.base_url === 'string' ? credentials.base_url : ''
  }

  /**
   * 校验 grok 表单 + 拼装 credentials。校验失败返回 null 并已 showError。
   * mode='create' 时 refresh_token 必填；mode='edit' 留空表示「保留现有值」。
   */
  function buildSubmitBundle(mode: 'create' | 'edit'): TkGrokCredentialsBundle | null {
    const trimmedRefresh = refreshToken.value.trim()
    if (mode === 'create' && !trimmedRefresh) {
      appStore.showError(t('admin.accounts.grokPlatform.pleaseEnterRefreshToken'))
      return null
    }

    const credentials: Record<string, unknown> = {}
    if (trimmedRefresh) {
      credentials.refresh_token = trimmedRefresh
    }
    const trimmedBase = baseUrl.value.trim()
    if (trimmedBase) {
      credentials.base_url = trimmedBase
    }

    return { credentials }
  }

  return {
    // refs
    refreshToken,
    baseUrl,
    // methods
    reset,
    populateFromAccount,
    buildSubmitBundle,
  }
}
