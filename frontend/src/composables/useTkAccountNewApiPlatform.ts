import { ref, computed, watch, type Ref, type ComputedRef } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { fetchUpstreamModels } from '@/api/admin/channels'
import { useNewApiChannelTypes } from '@/composables/useNewApiChannelTypes'
import { isNewApiUpstreamFetchableChannelType } from '@/constants/newApiUpstreamFetchableChannelTypes'
import { buildModelMappingObject } from '@/composables/useModelWhitelist'
import { unknownToErrorMessage } from '@/utils/authError'

// 上游大文件保持模板 + wiring：所有 newapi（第五平台）的添加 / 编辑 modal 业务
// 状态 + 副作用都收口在本 composable，让 CreateAccountModal / EditAccountModal
// 只剩「调用 + 透传 v-model」。
//
// 见 docs/accounts/newapi-add-account-ui-gap-analysis.md 与
// CLAUDE.md §5.x 「最小侵入 + composable 收口」。

export interface UseTkAccountNewApiPlatformOptions {
  /**
   * 当前 modal 是否真的处在 newapi 平台上下文：
   *   - CreateAccountModal: () => form.platform === 'newapi'
   *   - EditAccountModal:   () => account.platform === 'newapi'
   * 只读 getter 而非 boolean，方便随父组件状态变化。
   */
  isNewapi: () => boolean
  /**
   * 编辑模式才传：用于「获取模型列表」时空 api_key 走 stored credential 路径。
   * 后端 channel_handler_tk_newapi_admin.go 在 api_key 为空 + account_id 给定
   * 时会用 GetAccount(account_id).GetCredential("api_key") 兜底，避免编辑时
   * 必须重输密钥。Create modal 不传即可。
   */
  storedAccount?: () => { id?: number; channel_type?: number } | null
}

export interface TkNewApiCredentialsBundle {
  /** 选中的 channel_type 顶层字段（admin_service.go 强制 > 0） */
  channelType: number
  /** 待写入 credentials 的字段集合，已经处理好空值删除语义 */
  credentials: Record<string, unknown>
}

export interface TkNewApiAccountSnapshot {
  channel_type?: number
  credentials?: Record<string, unknown> | null
}

/**
 * NewAPI（第五平台）添加 / 编辑账号表单的全部状态与副作用。
 *
 * 暴露：
 *   - 所有 v-model 绑定的 ref（channelType / baseUrl / apiKey / restrictionMode /
 *     allowedModels / modelMappings / statusCodeMapping / openaiOrganization）
 *   - 给 AccountNewApiPlatformFields.vue 的 props（channelTypeOptions /
 *     channelTypesLoading / channelTypesError / selectedChannelTypeBaseUrl /
 *     fetchModelsEnabled / fetchModelsDisabled / fetchModelsLoading）
 *   - 给父组件调用的方法（bootstrap / reset / populateFromAccount /
 *     buildSubmitBundle / handleFetchUpstreamModels）
 */
export function useTkAccountNewApiPlatform(options: UseTkAccountNewApiPlatformOptions) {
  const { t } = useI18n()
  const appStore = useAppStore()
  const channelTypesCatalog = useNewApiChannelTypes()

  // ---- 表单字段 ----------------------------------------------------------
  const channelType = ref<number>(0)
  const baseUrl = ref('')
  const apiKey = ref('')
  // 已过期：原 raw JSON textarea 的输入容器；保留 ref 仅为给 v-model 透传，
  // 不再在 UI 上渲染（结构化 selector 才是 credentials.model_mapping 的唯一来源）。
  const modelMapping = ref('')
  const statusCodeMapping = ref('')
  const openaiOrganization = ref('')
  const allowedModels = ref<string[]>([])
  const modelMappings = ref<Array<{ from: string; to: string }>>([])
  const restrictionMode = ref<'whitelist' | 'mapping'>('whitelist')
  const fetchLoading = ref(false)

  // ---- 衍生 props --------------------------------------------------------
  const channelTypeOptions = computed(() =>
    channelTypesCatalog.types.value.map((c) => ({ value: c.channel_type, label: c.name }))
  )
  const selectedChannelTypeBaseUrl = computed(() => {
    const found = channelTypesCatalog.types.value.find((c) => c.channel_type === channelType.value)
    return found?.base_url || ''
  })
  const fetchModelsEnabled = computed(() => isNewApiUpstreamFetchableChannelType(channelType.value))
  const fetchModelsDisabled = computed(() => {
    const hasBase = (baseUrl.value.trim() || selectedChannelTypeBaseUrl.value).length > 0
    if (!hasBase) return true
    if (apiKey.value.trim()) return false
    // 编辑模式专用：当输入框留空 + 已有同 channel_type 的账号时，后端用
    // stored credential 兜底，因此按钮也启用。
    const stored = options.storedAccount?.()
    if (stored?.id && stored.channel_type === channelType.value) return false
    return true
  })

  // ---- channel_type 切换：自动 prefill base_url（与上游 new-api 一致，
  //      但只在用户尚未手填时覆盖，避免破坏私有 / 代理 base_url） -----------
  watch(
    () => channelType.value,
    (ct) => {
      if (!options.isNewapi() || !ct) return
      const found = channelTypesCatalog.types.value.find((c) => c.channel_type === ct)
      if (!found) return
      if (!baseUrl.value.trim()) {
        baseUrl.value = found.base_url || ''
      }
    }
  )

  // ---- 副作用：获取上游模型列表 ------------------------------------------
  async function handleFetchUpstreamModels(): Promise<void> {
    if (!channelType.value || channelType.value <= 0) {
      appStore.showError(t('admin.accounts.newApiPlatform.pleaseSelectChannelType'))
      return
    }
    const base = baseUrl.value.trim() || selectedChannelTypeBaseUrl.value
    const inputKey = apiKey.value.trim()
    const stored = options.storedAccount?.()
    const canUseStoredKey = !!stored?.id && stored?.channel_type === channelType.value
    if (!base || (!inputKey && !canUseStoredKey)) {
      appStore.showError(t('admin.accounts.newApiPlatform.fetchUpstreamModelsNeedUrlKey'))
      return
    }
    fetchLoading.value = true
    try {
      const models = await fetchUpstreamModels({
        base_url: base,
        channel_type: channelType.value,
        api_key: inputKey,
        ...(inputKey ? {} : { account_id: stored?.id }),
      })
      if (!models.length) {
        appStore.showInfo(t('admin.accounts.newApiPlatform.fetchUpstreamModelsEmpty'))
        return
      }
      // 拉到 N 个模型 → 强制切回 whitelist 模式；如果留在 mapping 模式会
      // 把 N 个模型变成 N 行无意义的 X→X 配对，淹没用户原本想填的特殊重命名。
      allowedModels.value = [...models]
      restrictionMode.value = 'whitelist'
      appStore.showSuccess(
        t('admin.accounts.newApiPlatform.fetchUpstreamModelsSuccess', { count: models.length })
      )
    } catch (e: unknown) {
      appStore.showError(
        unknownToErrorMessage(e, t('admin.accounts.newApiPlatform.fetchUpstreamModelsFailed'))
      )
    } finally {
      fetchLoading.value = false
    }
  }

  // ---- 生命周期辅助 ------------------------------------------------------
  /**
   * Modal 打开时调用：触发一次（已缓存）的 channel-type catalog 加载。幂等。
   */
  function bootstrap(): void {
    void channelTypesCatalog.load().catch(() => { /* 错误已写入 channelTypesCatalog.error */ })
  }

  /**
   * 重置全部 newapi 表单状态（CreateAccountModal.resetForm 调用）。
   */
  function reset(): void {
    channelType.value = 0
    baseUrl.value = ''
    apiKey.value = ''
    modelMapping.value = ''
    statusCodeMapping.value = ''
    openaiOrganization.value = ''
    allowedModels.value = []
    modelMappings.value = []
    restrictionMode.value = 'whitelist'
    fetchLoading.value = false
  }

  /**
   * 把已有 newapi 账号的字段镜像到本 composable 的 ref（EditAccountModal 用）。
   * 自动按 whitelist / mapping 模式推断结构化 selector 的初始模式。
   */
  function populateFromAccount(account: TkNewApiAccountSnapshot): void {
    const credentials = (account.credentials || {}) as Record<string, unknown>
    channelType.value = account.channel_type ?? 0
    baseUrl.value = (credentials.base_url as string) ?? ''
    apiKey.value = ''
    statusCodeMapping.value = typeof credentials.status_code_mapping === 'string'
      ? credentials.status_code_mapping
      : (credentials.status_code_mapping ? JSON.stringify(credentials.status_code_mapping) : '')
    openaiOrganization.value = typeof credentials.openai_organization === 'string'
      ? credentials.openai_organization
      : ''

    const existing = credentials.model_mapping
    if (existing && typeof existing === 'object' && !Array.isArray(existing)) {
      try {
        modelMapping.value = JSON.stringify(existing, null, 2)
      } catch {
        modelMapping.value = ''
      }
      const entries = Object.entries(existing as Record<string, unknown>)
        .filter(([, v]) => typeof v === 'string') as Array<[string, string]>
      if (entries.length === 0) {
        restrictionMode.value = 'whitelist'
        allowedModels.value = []
        modelMappings.value = []
      } else if (entries.every(([from, to]) => from === to)) {
        restrictionMode.value = 'whitelist'
        allowedModels.value = entries.map(([from]) => from)
        modelMappings.value = []
      } else {
        restrictionMode.value = 'mapping'
        allowedModels.value = []
        modelMappings.value = entries.map(([from, to]) => ({ from, to }))
      }
    } else {
      modelMapping.value = typeof existing === 'string' ? existing : ''
      restrictionMode.value = 'whitelist'
      allowedModels.value = []
      modelMappings.value = []
    }
  }

  /**
   * 校验 newapi 表单 + 拼装提交所需的 (channel_type, credentials)。
   * 校验失败时返回 null 并已 showError；调用方直接 return 即可。
   *
   * mode='create' 时 baseUrl 必填 + apiKey 必填；
   * mode='edit'   时 apiKey 留空表示「保留现有密钥」，由调用方决定如何 fall back。
   */
  function buildSubmitBundle(mode: 'create' | 'edit'): TkNewApiCredentialsBundle | null {
    if (!channelType.value || channelType.value <= 0) {
      appStore.showError(t('admin.accounts.newApiPlatform.pleaseSelectChannelType'))
      return null
    }
    const resolvedBase = baseUrl.value.trim() || selectedChannelTypeBaseUrl.value
    if (!resolvedBase) {
      appStore.showError(t('admin.accounts.newApiPlatform.pleaseEnterBaseUrl'))
      return null
    }
    const trimmedKey = apiKey.value.trim()
    if (mode === 'create' && !trimmedKey) {
      appStore.showError(t('admin.accounts.newApiPlatform.pleaseEnterApiKey'))
      return null
    }

    const credentials: Record<string, unknown> = {
      base_url: resolvedBase,
    }
    if (trimmedKey) {
      credentials.api_key = trimmedKey
    }

    const mapping = buildModelMappingObject(
      restrictionMode.value,
      allowedModels.value,
      modelMappings.value
    )
    if (mapping) {
      credentials.model_mapping = mapping
    }

    const statusTrim = statusCodeMapping.value.trim()
    if (statusTrim) {
      if (!isValidJsonObject(statusTrim)) {
        appStore.showError(t('admin.accounts.newApiPlatform.jsonObjectRequired'))
        return null
      }
      credentials.status_code_mapping = statusTrim
    }

    const orgTrim = openaiOrganization.value.trim()
    if (orgTrim) {
      credentials.openai_organization = orgTrim
    }

    return { channelType: channelType.value, credentials }
  }

  return {
    // refs
    channelType,
    baseUrl,
    apiKey,
    modelMapping,
    statusCodeMapping,
    openaiOrganization,
    allowedModels,
    modelMappings,
    restrictionMode,
    // computed props for AccountNewApiPlatformFields
    channelTypeOptions,
    channelTypesLoading: channelTypesCatalog.loading as Ref<boolean>,
    channelTypesError: channelTypesCatalog.error as Ref<string | null>,
    selectedChannelTypeBaseUrl: selectedChannelTypeBaseUrl as ComputedRef<string>,
    fetchModelsEnabled,
    fetchModelsDisabled,
    fetchModelsLoading: fetchLoading as Ref<boolean>,
    // methods
    bootstrap,
    reset,
    populateFromAccount,
    buildSubmitBundle,
    handleFetchUpstreamModels,
  }
}

// ---- 内部辅助 ------------------------------------------------------------

function isValidJsonObject(raw: string): boolean {
  try {
    const parsed = JSON.parse(raw)
    return parsed !== null && typeof parsed === 'object' && !Array.isArray(parsed)
  } catch {
    return false
  }
}
