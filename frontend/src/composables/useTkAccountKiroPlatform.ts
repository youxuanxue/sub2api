import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'

// 第六平台 Kiro 的添加 / 编辑 modal 业务状态 + 校验 + credentials 拼装都收口在
// 本 composable，让 CreateAccountModal 只剩「调用 + 透传 v-model」。
//
// 见 CLAUDE.md §5.x 「最小侵入 + composable 收口」。
//
// 账号 credentials 契约（后端已定）：platform=kiro, type=oauth
//   - access_token      (必填)
//   - refresh_token     (必填)
//   - region            (默认 us-east-1)
//   - auth_method       (必填，枚举 social | idc)
//   - machine_id        (可选，指纹用)
//   - client_id         (仅 auth_method=idc 时必填)
//   - client_secret     (仅 auth_method=idc 时必填)
//   - profile_arn       (可选，留空则后端自动获取)
//   - tos_acknowledged  (必填，后端强制为 true 才能创建)

export type KiroAuthMethod = 'social' | 'idc'

export const KIRO_DEFAULT_REGION = 'us-east-1'

export interface TkKiroCredentialsBundle {
  /** 待写入 credentials 的字段集合，已经处理好空值删除语义 */
  credentials: Record<string, unknown>
}

export interface TkKiroAccountSnapshot {
  credentials?: Record<string, unknown> | null
}

/**
 * Kiro（第六平台）添加 / 编辑账号表单的全部状态与副作用。
 *
 * 暴露：
 *   - 所有 v-model 绑定的 ref（accessToken / refreshToken / region / authMethod /
 *     machineId / clientId / clientSecret / profileArn / tosAcknowledged）
 *   - 给父组件调用的方法（reset / populateFromAccount / buildSubmitBundle）
 */
export function useTkAccountKiroPlatform() {
  const { t } = useI18n()
  const appStore = useAppStore()

  // ---- 表单字段 ----------------------------------------------------------
  const accessToken = ref('')
  const refreshToken = ref('')
  const region = ref(KIRO_DEFAULT_REGION)
  const authMethod = ref<KiroAuthMethod>('social')
  const machineId = ref('')
  const clientId = ref('')
  const clientSecret = ref('')
  const profileArn = ref('')
  const tosAcknowledged = ref(false)

  /**
   * 重置全部 kiro 表单状态（CreateAccountModal.resetForm 调用）。
   */
  function reset(): void {
    accessToken.value = ''
    refreshToken.value = ''
    region.value = KIRO_DEFAULT_REGION
    authMethod.value = 'social'
    machineId.value = ''
    clientId.value = ''
    clientSecret.value = ''
    profileArn.value = ''
    tosAcknowledged.value = false
  }

  /**
   * 把已有 kiro 账号的字段镜像到本 composable 的 ref（EditAccountModal 用）。
   * access_token / refresh_token / client_secret 都是敏感字段，编辑时留空表示
   * 「保留现有值」，因此不回填。
   */
  function populateFromAccount(account: TkKiroAccountSnapshot): void {
    const credentials = (account.credentials || {}) as Record<string, unknown>
    accessToken.value = ''
    refreshToken.value = ''
    clientSecret.value = ''
    region.value = typeof credentials.region === 'string' && credentials.region
      ? credentials.region
      : KIRO_DEFAULT_REGION
    authMethod.value = credentials.auth_method === 'idc' ? 'idc' : 'social'
    machineId.value = typeof credentials.machine_id === 'string' ? credentials.machine_id : ''
    clientId.value = typeof credentials.client_id === 'string' ? credentials.client_id : ''
    profileArn.value = typeof credentials.profile_arn === 'string' ? credentials.profile_arn : ''
    // 已存在的账号一定是带着 tos_acknowledged=true 创建的；编辑时默认勾选。
    tosAcknowledged.value = true
  }

  /**
   * 校验 kiro 表单 + 拼装提交所需的 credentials。
   * 校验失败时返回 null 并已 showError；调用方直接 return 即可。
   *
   * mode='create' 时 access_token / refresh_token 必填；
   * mode='edit'   时两者留空表示「保留现有值」，由调用方决定如何 fall back。
   */
  function buildSubmitBundle(mode: 'create' | 'edit'): TkKiroCredentialsBundle | null {
    if (!tosAcknowledged.value) {
      appStore.showError(t('admin.accounts.kiroPlatform.pleaseAcknowledgeTos'))
      return null
    }

    const trimmedAccess = accessToken.value.trim()
    const trimmedRefresh = refreshToken.value.trim()
    if (mode === 'create') {
      if (!trimmedAccess) {
        appStore.showError(t('admin.accounts.kiroPlatform.pleaseEnterAccessToken'))
        return null
      }
      if (!trimmedRefresh) {
        appStore.showError(t('admin.accounts.kiroPlatform.pleaseEnterRefreshToken'))
        return null
      }
    }

    if (authMethod.value === 'idc') {
      if (!clientId.value.trim()) {
        appStore.showError(t('admin.accounts.kiroPlatform.pleaseEnterClientId'))
        return null
      }
      if (mode === 'create' && !clientSecret.value.trim()) {
        appStore.showError(t('admin.accounts.kiroPlatform.pleaseEnterClientSecret'))
        return null
      }
    }

    const credentials: Record<string, unknown> = {
      region: region.value.trim() || KIRO_DEFAULT_REGION,
      auth_method: authMethod.value,
      tos_acknowledged: true,
    }
    if (trimmedAccess) {
      credentials.access_token = trimmedAccess
    }
    if (trimmedRefresh) {
      credentials.refresh_token = trimmedRefresh
    }

    const machineTrim = machineId.value.trim()
    if (machineTrim) {
      credentials.machine_id = machineTrim
    }

    const profileTrim = profileArn.value.trim()
    if (profileTrim) {
      credentials.profile_arn = profileTrim
    }

    if (authMethod.value === 'idc') {
      credentials.client_id = clientId.value.trim()
      const secretTrim = clientSecret.value.trim()
      if (secretTrim) {
        credentials.client_secret = secretTrim
      }
    }

    return { credentials }
  }

  return {
    // refs
    accessToken,
    refreshToken,
    region,
    authMethod,
    machineId,
    clientId,
    clientSecret,
    profileArn,
    tosAcknowledged,
    // methods
    reset,
    populateFromAccount,
    buildSubmitBundle,
  }
}
