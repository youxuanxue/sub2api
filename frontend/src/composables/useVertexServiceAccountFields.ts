import { computed, ref } from 'vue'
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
    location.value = options.defaultLocation ?? VERTEX_SA_DEFAULT_LOCATION
    jsonInput.value = ''
    clearDerived()
    dragActive.value = false
  }

  function populateFromCredentials(credentials: Record<string, unknown> | undefined | null) {
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

  function buildCredentialsForCreate(): Record<string, unknown> | null {
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
    credentialsStatus?: { has_service_account_json?: boolean; has_service_account?: boolean } | null
  ): Record<string, unknown> | null {
    if (!location.value.trim()) {
      appStore.showError(t('admin.accounts.vertexLocationRequired'))
      return null
    }

    const next: Record<string, unknown> = { ...currentCredentials }
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
