import { computed, nextTick, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import {
  accountHasStoredVertexServiceAccountJson,
  buildVertexServiceAccountCredentials,
  parseVertexServiceAccountJsonInput,
  VertexServiceAccountParseError,
  type ParsedVertexServiceAccount
} from '@/utils/vertexServiceAccount'

export const VERTEX_SA_DEFAULT_LOCATION = 'us-central1'
/** API Key 认证的默认 location（upstream 2026-10：Vertex API Key 默认 global）。 */
export const VERTEX_APIKEY_DEFAULT_LOCATION = 'global'

export type VertexAuthMode = 'service_account' | 'apikey'

export interface UseVertexServiceAccountFieldsOptions {
  defaultLocation?: string
}

/**
 * Shared Vertex Service Account form state for CreateAccountModal and EditAccountModal.
 * UI lives in VertexServiceAccountFields.vue; parsing/submit helpers live here.
 */
export function useVertexServiceAccountFields(options: UseVertexServiceAccountFieldsOptions = {}) {
  const { t } = useI18n()
  const appStore = useAppStore()

  const location = ref(options.defaultLocation ?? VERTEX_SA_DEFAULT_LOCATION)
  const projectId = ref('')
  const clientEmail = ref('')
  /** Textarea / last-applied normalized JSON used on create or JSON rotation. */
  const jsonInput = ref('')
  const normalizedJson = ref('')
  const dragActive = ref(false)
  const fileInputRef = ref<HTMLInputElement | null>(null)

  // API Key 认证（upstream 2026-10）：仅 Gemini 平台的 Vertex 账号可选；Anthropic
  // 平台的 Vertex 账号始终使用 Service Account JSON（模态框据此决定是否展示切换）。
  const authMode = ref<VertexAuthMode>('service_account')
  const apiKey = ref('')
  const isApiKeyMode = computed(() => authMode.value === 'apikey')

  // 两种认证方式的 Project ID 来源不同（JSON 读取 / 手填），切换时清空凭证字段避免串用。
  // populateFromCredentials 回填（编辑场景）不触发清空。
  let populating = false
  watch(authMode, (mode) => {
    if (populating) return
    jsonInput.value = ''
    apiKey.value = ''
    clearDerived()
    location.value =
      mode === 'apikey'
        ? VERTEX_APIKEY_DEFAULT_LOCATION
        : (options.defaultLocation ?? VERTEX_SA_DEFAULT_LOCATION)
  })

  const isLoaded = computed(() => Boolean(clientEmail.value.trim()))

  function clearDerived() {
    projectId.value = ''
    clientEmail.value = ''
    normalizedJson.value = ''
  }

  function applyParsed(parsed: ParsedVertexServiceAccount, opts: { rewriteInput?: boolean } = {}) {
    projectId.value = parsed.projectId
    clientEmail.value = parsed.clientEmail
    normalizedJson.value = parsed.normalizedJson
    if (opts.rewriteInput !== false) {
      jsonInput.value = parsed.normalizedJson
    }
  }

  function applyJson(
    raw: string,
    opts: { silent?: boolean; rewriteInput?: boolean } = {}
  ): boolean {
    const { silent = false, rewriteInput = true } = opts
    if (!raw.trim()) {
      clearDerived()
      return false
    }
    try {
      applyParsed(parseVertexServiceAccountJsonInput(raw), { rewriteInput })
      return true
    } catch (err) {
      if (!silent && err instanceof VertexServiceAccountParseError) {
        appStore.showError(t(err.i18nKey))
      }
      return false
    }
  }

  function previewJsonInput(): void {
    const raw = jsonInput.value.trim()
    if (raw) applyJson(raw, { silent: true, rewriteInput: false })
  }

  async function handleFileChange(event: Event): Promise<void> {
    const input = event.target as HTMLInputElement
    const file = input.files?.[0]
    if (!file) return
    try {
      applyJson(await file.text())
    } finally {
      input.value = ''
    }
  }

  async function handleFileDrop(event: DragEvent): Promise<void> {
    dragActive.value = false
    const file = event.dataTransfer?.files?.[0]
    if (!file) return
    applyJson(await file.text())
  }

  function reset() {
    authMode.value = 'service_account'
    apiKey.value = ''
    location.value = options.defaultLocation ?? VERTEX_SA_DEFAULT_LOCATION
    jsonInput.value = ''
    clearDerived()
    dragActive.value = false
  }

  function populateFromCredentials(credentials: Record<string, unknown> | undefined | null) {
    populating = true
    authMode.value = credentials?.auth_mode === 'apikey' ? 'apikey' : 'service_account'
    apiKey.value = '' // API Key 已脱敏不再随 credentials 返回；留空表示保留原 Key。
    jsonInput.value = ''
    normalizedJson.value = ''
    projectId.value = typeof credentials?.project_id === 'string' ? credentials.project_id.trim() : ''
    clientEmail.value =
      typeof credentials?.client_email === 'string' ? credentials.client_email.trim() : ''
    const loc =
      (typeof credentials?.location === 'string' && credentials.location.trim()) ||
      (typeof credentials?.vertex_location === 'string' && credentials.vertex_location.trim()) ||
      ''
    location.value = loc || VERTEX_SA_DEFAULT_LOCATION
    // watch(authMode) 是 pre-flush 异步执行，等它跑完再解除回填保护。
    void nextTick(() => {
      populating = false
    })
  }

  function validateForCreate(): boolean {
    if (!location.value.trim()) {
      appStore.showError(t('admin.accounts.vertexLocationRequired'))
      return false
    }
    const raw = jsonInput.value.trim() || normalizedJson.value.trim()
    if (!applyJson(raw)) {
      if (!raw.trim()) {
        appStore.showError(t('admin.accounts.vertexSaJsonRequired'))
      }
      return false
    }
    return true
  }

  function validateApiKeyForCreate(): boolean {
    if (!apiKey.value.trim()) {
      appStore.showError(t('admin.accounts.vertexApiKeyRequired'))
      return false
    }
    if (projectId.value.trim() && !location.value.trim()) {
      appStore.showError(t('admin.accounts.vertexLocationRequired'))
      return false
    }
    return true
  }

  function buildApiKeyCredentials(): Record<string, unknown> {
    const credentials: Record<string, unknown> = {
      auth_mode: 'apikey',
      api_key: apiKey.value.trim(),
      location: location.value.trim() || VERTEX_APIKEY_DEFAULT_LOCATION,
      tier_id: 'vertex'
    }
    if (projectId.value.trim()) {
      credentials.project_id = projectId.value.trim()
    }
    return credentials
  }

  function buildCredentialsForCreate(): Record<string, unknown> | null {
    if (isApiKeyMode.value) {
      if (!validateApiKeyForCreate()) return null
      return buildApiKeyCredentials()
    }
    if (!validateForCreate()) return null
    return buildVertexServiceAccountCredentials(
      {
        projectId: projectId.value.trim(),
        clientEmail: clientEmail.value.trim(),
        normalizedJson: normalizedJson.value.trim()
      },
      location.value
    )
  }

  /**
   * Edit path: empty jsonInput keeps stored SA JSON; non-empty rotates credentials.
   */
  function mergeCredentialsForEdit(
    currentCredentials: Record<string, unknown>,
    credentialsStatus?: {
      has_service_account_json?: boolean
      has_service_account?: boolean
      has_api_key?: boolean
    } | null
  ): Record<string, unknown> | null {
    const next: Record<string, unknown> = { ...currentCredentials }

    if (isApiKeyMode.value) {
      // API Key 已脱敏不再随 credentials 返回，存在性读 credentials_status；留空表示保留原 Key。
      const newApiKey = apiKey.value.trim()
      const hasExistingApiKey = credentialsStatus
        ? Boolean(credentialsStatus.has_api_key)
        : Boolean(currentCredentials.api_key)
      if (!newApiKey && !hasExistingApiKey) {
        appStore.showError(t('admin.accounts.vertexApiKeyRequired'))
        return null
      }
      if (projectId.value.trim() && !location.value.trim()) {
        appStore.showError(t('admin.accounts.vertexLocationRequired'))
        return null
      }
      if (newApiKey) {
        next.api_key = newApiKey
      }
      if (projectId.value.trim()) {
        next.project_id = projectId.value.trim()
      } else {
        delete next.project_id
      }
      next.location = location.value.trim() || VERTEX_APIKEY_DEFAULT_LOCATION
      next.auth_mode = 'apikey'
      next.tier_id = 'vertex'
      return next
    }

    if (!location.value.trim()) {
      appStore.showError(t('admin.accounts.vertexLocationRequired'))
      return null
    }

    const saJsonInput = jsonInput.value.trim()

    if (saJsonInput) {
      if (!applyJson(saJsonInput)) return null
      Object.assign(next, buildVertexServiceAccountCredentials(
        {
          projectId: projectId.value.trim(),
          clientEmail: clientEmail.value.trim(),
          normalizedJson: normalizedJson.value.trim()
        },
        location.value
      ))
    } else {
      const hasExisting = accountHasStoredVertexServiceAccountJson(
        currentCredentials,
        credentialsStatus ?? undefined
      )
      if (!hasExisting) {
        appStore.showError(t('admin.accounts.vertexSaJsonRequired'))
        return null
      }
      if (!projectId.value.trim()) {
        appStore.showError(t('admin.accounts.vertexSaJsonMissingProjectId'))
        return null
      }
      if (!clientEmail.value.trim()) {
        appStore.showError(t('admin.accounts.vertexSaJsonMissingClientEmail'))
        return null
      }
      next.project_id = projectId.value.trim()
      next.client_email = clientEmail.value.trim()
      next.location = location.value.trim()
      next.tier_id = 'vertex'
    }

    return next
  }

  return {
    location,
    projectId,
    clientEmail,
    jsonInput,
    normalizedJson,
    dragActive,
    fileInputRef,
    isLoaded,
    authMode,
    apiKey,
    isApiKeyMode,
    applyJson,
    previewJsonInput,
    handleFileChange,
    handleFileDrop,
    reset,
    populateFromCredentials,
    validateForCreate,
    buildCredentialsForCreate,
    mergeCredentialsForEdit
  }
}

export type VertexServiceAccountFieldBag = ReturnType<typeof useVertexServiceAccountFields>
