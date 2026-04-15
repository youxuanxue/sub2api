import { computed, watch, unref, type Ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { adminAPI } from '@/api/admin'
import { useNewApiChannelTypes } from '@/composables/useNewApiChannelTypes'
import { useNewApiChannelTypeModels } from '@/composables/useNewApiChannelTypeModels'
import { isNewApiUpstreamFetchableChannelType } from '@/constants/newApiUpstreamFetchableChannelTypes'
import { unknownToErrorMessage } from '@/utils/authError'

/** Minimal form shape for New API channel_type binding */
export interface TkNewApiPlatformFormShape {
  channel_type: number
}

export interface UseTkAccountNewApiPlatformOptions {
  form: TkNewApiPlatformFormShape
  isNewapi: () => boolean
  baseUrl: Ref<string>
  apiKey: Ref<string>
  /** Edit account only: when set, fetch upstream models may use stored api_key if the field is left empty. */
  accountId?: Ref<number | undefined>
  allowedModels: Ref<string[]>
  lastUpstreamModels: Ref<string[] | null>
  fetchLoading: Ref<boolean>
  /**
   * When false, channel_type changes still align base_url from catalog but skip
   * loading adaptor model lists into the allowlist (Edit modal defers until account sync completes).
   */
  shouldSyncAllowedModelsFromChannelType?: () => boolean
}

/**
 * TokenKey New API (fifth platform) channel catalog, preset models, and upstream-model fetch — shared by create/edit account modals.
 */
export function useTkAccountNewApiPlatform(options: UseTkAccountNewApiPlatformOptions) {
  const { t } = useI18n()
  const appStore = useAppStore()
  const channelTypes = useNewApiChannelTypes()
  const channelTypeModels = useNewApiChannelTypeModels()

  const channelTypesLoading = computed(() => unref(channelTypes.loading))
  const channelTypesError = computed(() => unref(channelTypes.error))

  const fillPresets = computed(() => {
    if (!options.isNewapi() || !options.form.channel_type) return undefined
    const raw = channelTypeModels.map.value[String(options.form.channel_type)]
    if (Array.isArray(raw) && raw.length > 0) return [...raw]
    const up = options.lastUpstreamModels.value
    if (Array.isArray(up) && up.length > 0) return [...up]
    return undefined
  })

  async function fetchUpstreamModels(): Promise<void> {
    if (!options.isNewapi() || !options.form.channel_type) return
    const base = options.baseUrl.value.trim()
    const key = options.apiKey.value.trim()
    const aid = options.accountId?.value
    const canUseStoredKey = aid != null && aid > 0
    if (!base || (!key && !canUseStoredKey)) {
      appStore.showError(t('admin.accounts.newApiPlatform.fetchUpstreamModelsNeedUrlKey'))
      return
    }
    options.fetchLoading.value = true
    try {
      const models = await adminAPI.channels.fetchUpstreamModels({
        base_url: base,
        channel_type: options.form.channel_type,
        api_key: key,
        ...(canUseStoredKey ? { account_id: aid } : {})
      })
      if (!models.length) {
        appStore.showInfo(t('admin.accounts.newApiPlatform.fetchUpstreamModelsEmpty'))
        return
      }
      options.lastUpstreamModels.value = [...models]
      options.allowedModels.value = [...models]
      appStore.showSuccess(t('admin.accounts.newApiPlatform.fetchUpstreamModelsSuccess', { count: models.length }))
    } catch (e: unknown) {
      appStore.showError(unknownToErrorMessage(e, t('admin.accounts.newApiPlatform.fetchUpstreamModelsFailed')))
    } finally {
      options.fetchLoading.value = false
    }
  }

  const upstreamFetchConfig = computed(() => ({
    show:
      options.isNewapi() &&
      !!options.form.channel_type &&
      isNewApiUpstreamFetchableChannelType(options.form.channel_type),
    loading: options.fetchLoading.value,
    disabled:
      !options.baseUrl.value.trim() ||
      (!options.apiKey.value.trim() &&
        !(options.accountId?.value != null && options.accountId.value > 0)),
    onFetch: fetchUpstreamModels
  }))

  const channelTypeOptions = computed(() => {
    const types = unref(channelTypes.types)
    if (!types) return []
    return types.map((ct) => ({
      value: ct.channel_type,
      label: `${ct.name} (${ct.channel_type})`
    }))
  })

  const selectedChannelTypeBaseUrl = computed(() => {
    if (!options.form.channel_type) return ''
    const types = unref(channelTypes.types)
    const match = types.find((x) => x.channel_type === options.form.channel_type)
    return match?.base_url ?? ''
  })

  function bootstrapNewapiCatalog(): void {
    channelTypes.load().catch(() => {})
    channelTypeModels.load().catch(() => {})
  }

  watch(
    () => options.form.channel_type,
    async (ct) => {
      if (!options.isNewapi() || !ct) return
      options.lastUpstreamModels.value = null
      const types = unref(channelTypes.types)
      const match = types.find((x) => x.channel_type === ct)
      if (match?.base_url) {
        options.baseUrl.value = match.base_url
      }
      const sync = options.shouldSyncAllowedModelsFromChannelType?.() ?? true
      if (!sync) return
      await channelTypeModels.load()
      const list = channelTypeModels.map.value[String(ct)]
      if (Array.isArray(list) && list.length > 0) {
        options.allowedModels.value = [...list]
      } else {
        options.allowedModels.value = []
      }
    }
  )

  return {
    channelTypes,
    channelTypeModels,
    channelTypesLoading,
    channelTypesError,
    fillPresets,
    upstreamFetchConfig,
    channelTypeOptions,
    selectedChannelTypeBaseUrl,
    bootstrapNewapiCatalog,
    fetchUpstreamModels
  }
}
