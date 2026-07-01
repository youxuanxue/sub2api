import { computed, ref, type ComputedRef, type Ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import {
  accountHasStoredKiroRegistration,
  accountHasStoredKiroTokens,
  KiroOAuthParseError,
  parseKiroRegistrationJsonInput,
  parseKiroTokenJsonInput,
  type KiroAuthMethod,
  type ParsedKiroRegistrationJson,
  type ParsedKiroTokenJson
} from '@/utils/kiroOAuthCredentials'

// 第六平台 Kiro 的添加 / 编辑 modal 业务状态 + 校验 + credentials 拼装都收口在
// 本 composable，让 CreateAccountModal 只剩「调用 + 透传 fields」。

export type { KiroAuthMethod }

export const KIRO_DEFAULT_REGION = 'us-east-1'

export interface TkKiroCredentialsBundle {
  credentials: Record<string, unknown>
}

export interface TkKiroAccountSnapshot {
  credentials?: Record<string, unknown> | null
  credentials_status?: Record<string, boolean> | null
}

export interface TkKiroBuildSubmitOptions {
  credentialsStatus?: Record<string, boolean> | null
}

export interface KiroPlatformFieldBag {
  tokenJsonInput: Ref<string>
  registrationJsonInput: Ref<string>
  region: Ref<string>
  authMethod: Ref<KiroAuthMethod>
  machineId: Ref<string>
  profileArn: Ref<string>
  tosAcknowledged: Ref<boolean>
  tokenLoaded: ComputedRef<boolean>
  registrationLoaded: ComputedRef<boolean>
  registrationClientIdPreview: ComputedRef<string>
  previewTokenJsonInput: () => void
  previewRegistrationJsonInput: () => void
}

export function useTkAccountKiroPlatform() {
  const { t } = useI18n()
  const appStore = useAppStore()

  const tokenJsonInput = ref('')
  const registrationJsonInput = ref('')
  const region = ref(KIRO_DEFAULT_REGION)
  const authMethod = ref<KiroAuthMethod>('social')
  const machineId = ref('')
  const profileArn = ref('')
  const tosAcknowledged = ref(false)

  const parsedAccessToken = ref('')
  const parsedRefreshToken = ref('')
  const parsedRegistration = ref<ParsedKiroRegistrationJson | null>(null)
  const storedRegistrationPresent = ref(false)

  const tokenLoaded = computed(
    () => Boolean(parsedAccessToken.value.trim() && parsedRefreshToken.value.trim())
  )
  const registrationLoaded = computed(() => Boolean(parsedRegistration.value?.clientId))
  const registrationClientIdPreview = computed(() => parsedRegistration.value?.clientId ?? '')

  function clearTokenDerived() {
    parsedAccessToken.value = ''
    parsedRefreshToken.value = ''
  }

  function clearRegistrationDerived() {
    parsedRegistration.value = null
  }

  function applyParsedToken(parsed: ParsedKiroTokenJson, opts: { updateMeta?: boolean } = {}) {
    const { updateMeta = true } = opts
    parsedAccessToken.value = parsed.accessToken
    parsedRefreshToken.value = parsed.refreshToken
    if (updateMeta) {
      region.value = parsed.region
      authMethod.value = parsed.authMethod
    }
  }

  function applyTokenJson(
    raw: string,
    opts: { silent?: boolean; rewriteInput?: boolean; updateMeta?: boolean } = {}
  ): boolean {
    const { silent = false, rewriteInput = true, updateMeta = true } = opts
    if (!raw.trim()) {
      clearTokenDerived()
      return false
    }
    try {
      applyParsedToken(parseKiroTokenJsonInput(raw), { updateMeta })
      if (rewriteInput) {
        tokenJsonInput.value = raw.trim()
      }
      return true
    } catch (err) {
      if (!silent && err instanceof KiroOAuthParseError) {
        appStore.showError(t(err.i18nKey))
      }
      return false
    }
  }

  function applyRegistrationJson(
    raw: string,
    opts: { silent?: boolean; rewriteInput?: boolean } = {}
  ): boolean {
    const { silent = false, rewriteInput = true } = opts
    if (!raw.trim()) {
      clearRegistrationDerived()
      return false
    }
    try {
      parsedRegistration.value = parseKiroRegistrationJsonInput(raw)
      if (rewriteInput) {
        registrationJsonInput.value = raw.trim()
      }
      return true
    } catch (err) {
      if (!silent && err instanceof KiroOAuthParseError) {
        appStore.showError(t(err.i18nKey))
      }
      return false
    }
  }

  function previewTokenJsonInput(): void {
    const raw = tokenJsonInput.value.trim()
    if (raw) {
      applyTokenJson(raw, { silent: true, rewriteInput: false })
    }
  }

  function previewRegistrationJsonInput(): void {
    const raw = registrationJsonInput.value.trim()
    if (raw) {
      applyRegistrationJson(raw, { silent: true, rewriteInput: false })
    }
  }

  function reset(): void {
    tokenJsonInput.value = ''
    registrationJsonInput.value = ''
    clearTokenDerived()
    clearRegistrationDerived()
    region.value = KIRO_DEFAULT_REGION
    authMethod.value = 'social'
    machineId.value = ''
    profileArn.value = ''
    tosAcknowledged.value = false
    storedRegistrationPresent.value = false
  }

  function populateFromAccount(account: TkKiroAccountSnapshot): void {
    const credentials = (account.credentials || {}) as Record<string, unknown>
    tokenJsonInput.value = ''
    registrationJsonInput.value = ''
    clearTokenDerived()
    clearRegistrationDerived()
    region.value =
      typeof credentials.region === 'string' && credentials.region
        ? credentials.region
        : KIRO_DEFAULT_REGION
    authMethod.value = credentials.auth_method === 'idc' ? 'idc' : 'social'
    machineId.value = typeof credentials.machine_id === 'string' ? credentials.machine_id : ''
    profileArn.value = typeof credentials.profile_arn === 'string' ? credentials.profile_arn : ''
    tosAcknowledged.value = true
    storedRegistrationPresent.value = accountHasStoredKiroRegistration(
      credentials,
      account.credentials_status
    )
  }

  function buildSubmitBundle(
    mode: 'create' | 'edit',
    options: TkKiroBuildSubmitOptions = {}
  ): TkKiroCredentialsBundle | null {
    if (!tosAcknowledged.value) {
      appStore.showError(t('admin.accounts.kiroPlatform.pleaseAcknowledgeTos'))
      return null
    }

    const tokenRaw = tokenJsonInput.value.trim()
    if (tokenRaw) {
      if (!applyTokenJson(tokenRaw, { updateMeta: false })) {
        return null
      }
    } else if (mode === 'create') {
      appStore.showError(t('admin.accounts.kiroPlatform.pleasePasteTokenJson'))
      return null
    }

    const registrationRaw = registrationJsonInput.value.trim()
    if (registrationRaw) {
      if (!applyRegistrationJson(registrationRaw)) {
        return null
      }
    }

    const effectiveAuthMethod = authMethod.value
    const hasStoredTokens = accountHasStoredKiroTokens(options.credentialsStatus)
    const hasStoredRegistration =
      storedRegistrationPresent.value ||
      accountHasStoredKiroRegistration(undefined, options.credentialsStatus)

    if (mode === 'create' && !tokenLoaded.value) {
      appStore.showError(t('admin.accounts.kiroPlatform.pleasePasteTokenJson'))
      return null
    }

    if (effectiveAuthMethod === 'idc') {
      if (mode === 'create' && !registrationLoaded.value) {
        appStore.showError(t('admin.accounts.kiroPlatform.pleasePasteRegistrationJson'))
        return null
      }
      if (mode === 'edit' && !registrationLoaded.value && !hasStoredRegistration) {
        appStore.showError(t('admin.accounts.kiroPlatform.pleasePasteRegistrationJson'))
        return null
      }
    }

    if (mode === 'edit' && !tokenLoaded.value && !hasStoredTokens) {
      // Non-secret fields only; tokens unchanged on server.
    }

    const credentials: Record<string, unknown> = {
      region: region.value.trim() || KIRO_DEFAULT_REGION,
      auth_method: effectiveAuthMethod,
      tos_acknowledged: true
    }

    if (parsedAccessToken.value.trim()) {
      credentials.access_token = parsedAccessToken.value.trim()
    }
    if (parsedRefreshToken.value.trim()) {
      credentials.refresh_token = parsedRefreshToken.value.trim()
    }

    const machineTrim = machineId.value.trim()
    if (machineTrim) {
      credentials.machine_id = machineTrim
    }

    const profileTrim = profileArn.value.trim()
    if (profileTrim) {
      credentials.profile_arn = profileTrim
    }

    if (effectiveAuthMethod === 'idc' && parsedRegistration.value) {
      credentials.client_id = parsedRegistration.value.clientId
      credentials.client_secret = parsedRegistration.value.clientSecret
    }

    return { credentials }
  }

  const fields: KiroPlatformFieldBag = {
    tokenJsonInput,
    registrationJsonInput,
    region,
    authMethod,
    machineId,
    profileArn,
    tosAcknowledged,
    tokenLoaded,
    registrationLoaded,
    registrationClientIdPreview,
    previewTokenJsonInput,
    previewRegistrationJsonInput
  }

  return {
    fields,
    reset,
    populateFromAccount,
    buildSubmitBundle
  }
}
