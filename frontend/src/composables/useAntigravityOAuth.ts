import { getCurrentScope, onScopeDispose, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { adminAPI } from '@/api/admin'
import type { AntigravityTokenInfo } from '@/api/admin/antigravity'

export function useAntigravityOAuth() {
  const appStore = useAppStore()
  const { t } = useI18n()

  const authUrl = ref('')
  const sessionId = ref('')
  const state = ref('')
  const loading = ref(false)
  const error = ref('')
  let generation = 0
  let expiryTimer: ReturnType<typeof setTimeout> | undefined

  const clearExpiry = () => {
    if (expiryTimer) clearTimeout(expiryTimer)
    expiryTimer = undefined
  }
  const clearSession = () => {
    authUrl.value = ''
    sessionId.value = ''
    state.value = ''
  }

  const resetState = () => {
    generation++
    clearExpiry()
    clearSession()
    loading.value = false
    error.value = ''
  }

  if (getCurrentScope()) onScopeDispose(resetState)

  const generateAuthUrl = async (proxyId: number | null | undefined): Promise<boolean> => {
    resetState()
    const current = generation
    loading.value = true

    try {
      const payload: Record<string, unknown> = {}
      if (proxyId) payload.proxy_id = proxyId

      const response = await adminAPI.antigravity.generateAuthUrl(payload as any)
      if (current !== generation) return false
      // Older edges omit expires_at; their SessionStore still uses a 30 min TTL.
      const remaining = response.expires_at === undefined ? 30 * 60_000 : response.expires_at * 1000 - Date.now()
      const expire = () => {
        clearSession()
        error.value = t('admin.accounts.antigravityAuthExpired')
      }
      if (remaining <= 0) {
        expire()
        return false
      }
      expiryTimer = setTimeout(expire, remaining)
      authUrl.value = response.auth_url
      sessionId.value = response.session_id
      state.value = response.state
      return true
    } catch (err: any) {
      if (current !== generation) return false
      error.value =
        err.response?.data?.detail || err.message || t('admin.accounts.oauth.antigravity.failedToGenerateUrl')
      appStore.showError(error.value)
      return false
    } finally {
      if (current === generation) loading.value = false
    }
  }

  const exchangeAuthCode = async (params: {
    code: string
    sessionId: string
    state: string
    proxyId?: number | null
  }): Promise<AntigravityTokenInfo | null> => {
    const code = params.code?.trim()
    if (!code || !params.sessionId || !params.state) {
      error.value = t('admin.accounts.oauth.antigravity.missingExchangeParams')
      return null
    }

    const current = generation
    loading.value = true
    error.value = ''

    try {
      const payload: Record<string, unknown> = {
        session_id: params.sessionId,
        state: params.state,
        code
      }
      if (params.proxyId) payload.proxy_id = params.proxyId

      const tokenInfo = await adminAPI.antigravity.exchangeCode(payload as any)
      if (current !== generation) return null
      return tokenInfo as AntigravityTokenInfo
    } catch (err: any) {
      if (current !== generation) return null
      error.value =
        err.response?.data?.detail || t('admin.accounts.oauth.antigravity.failedToExchangeCode')
      appStore.showError(error.value)
      return null
    } finally {
      if (current === generation) loading.value = false
    }
  }

  const validateRefreshToken = async (
    refreshToken: string,
    proxyId?: number | null
  ): Promise<AntigravityTokenInfo | null> => {
    if (!refreshToken.trim()) {
      error.value = t('admin.accounts.oauth.antigravity.pleaseEnterRefreshToken')
      return null
    }

    loading.value = true
    error.value = ''

    try {
      const tokenInfo = await adminAPI.antigravity.refreshAntigravityToken(
        refreshToken.trim(),
        proxyId
      )
      return tokenInfo as AntigravityTokenInfo
    } catch (err: any) {
      error.value =
        err.response?.data?.detail || t('admin.accounts.oauth.antigravity.failedToValidateRT')
      // Don't show global error toast for batch validation to avoid spamming
      // appStore.showError(error.value)
      return null
    } finally {
      loading.value = false
    }
  }

  const buildCredentials = (
    tokenInfo: AntigravityTokenInfo,
    fallbackRefreshToken?: string
  ): Record<string, unknown> => {
    let expiresAt: string | undefined
    if (typeof tokenInfo.expires_at === 'number' && Number.isFinite(tokenInfo.expires_at)) {
      expiresAt = Math.floor(tokenInfo.expires_at).toString()
    } else if (typeof tokenInfo.expires_at === 'string' && tokenInfo.expires_at.trim()) {
      expiresAt = tokenInfo.expires_at.trim()
    }
    const refreshToken = tokenInfo.refresh_token?.trim()
      ? tokenInfo.refresh_token
      : fallbackRefreshToken

    return {
      access_token: tokenInfo.access_token,
      refresh_token: refreshToken,
      token_type: tokenInfo.token_type,
      expires_at: expiresAt,
      project_id: tokenInfo.project_id,
      email: tokenInfo.email,
      ...(tokenInfo.plan_type ? { plan_type: tokenInfo.plan_type } : {})
    }
  }

  return {
    authUrl,
    sessionId,
    state,
    loading,
    error,
    resetState,
    generateAuthUrl,
    exchangeAuthCode,
    validateRefreshToken,
    buildCredentials
  }
}
