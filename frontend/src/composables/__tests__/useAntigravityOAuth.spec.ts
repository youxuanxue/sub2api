import { afterEach, describe, expect, it, vi } from 'vitest'
import { adminAPI } from '@/api/admin'

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn()
  })
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key
  })
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    antigravity: {
      generateAuthUrl: vi.fn(),
      exchangeCode: vi.fn(),
      refreshAntigravityToken: vi.fn()
    }
  }
}))

import { useAntigravityOAuth } from '@/composables/useAntigravityOAuth'

describe('useAntigravityOAuth.buildCredentials', () => {
  it('falls back to the submitted refresh token when the response omits it', () => {
    const oauth = useAntigravityOAuth()

    const credentials = oauth.buildCredentials(
      {
        access_token: 'access-token',
        expires_at: 1_900_000_000
      },
      'submitted-refresh-token'
    )

    expect(credentials.refresh_token).toBe('submitted-refresh-token')
  })

  it('prefers a new refresh token returned by the response', () => {
    const oauth = useAntigravityOAuth()

    const credentials = oauth.buildCredentials(
      {
        access_token: 'access-token',
        refresh_token: 'rotated-refresh-token',
        expires_at: 1_900_000_000
      },
      'submitted-refresh-token'
    )

    expect(credentials.refresh_token).toBe('rotated-refresh-token')
  })
})

// This builder is shared by OAuth creation, refresh-token import and reauthorization.
describe('Antigravity subscription metadata', () => {
  it.each(['pro', 'ultra', 'free'])('preserves the upstream %s plan when saving credentials', (plan) => {
    const oauth = useAntigravityOAuth()
    const credentials = oauth.buildCredentials({ plan_type: plan })
    expect(credentials.plan_type).toBe(plan)
  })

  it('does not invent a plan when subscription discovery is unavailable', () => {
    const credentials = useAntigravityOAuth().buildCredentials({})
    expect(credentials).not.toHaveProperty('plan_type')
  })
})


describe('Antigravity authorization link lifecycle', () => {
  afterEach(() => { vi.useRealTimers(); vi.clearAllMocks() })
  const result = { auth_url: 'https://accounts.google.com/o/oauth2/v2/auth?state=fixture', session_id: 'session-fixture', state: 'fixture' }

  it('uses the account proxy and clears all session fields at server expiry', async () => {
    vi.useFakeTimers()
    const expires = Math.floor(Date.now() / 1000) + 60
    vi.mocked(adminAPI.antigravity.generateAuthUrl).mockResolvedValue({ ...result, expires_at: expires })
    const oauth = useAntigravityOAuth()
    expect(await oauth.generateAuthUrl(7)).toBe(true)
    expect(adminAPI.antigravity.generateAuthUrl).toHaveBeenCalledWith({ proxy_id: 7 })
    expect([oauth.authUrl.value, oauth.sessionId.value, oauth.state.value]).toEqual([result.auth_url, result.session_id, result.state])
    vi.advanceTimersByTime(60_000)
    expect([oauth.authUrl.value, oauth.sessionId.value, oauth.state.value]).toEqual(['', '', ''])
    expect(oauth.error.value).toBe('admin.accounts.antigravityAuthExpired')
  })

  it('does not restore a closed account dialog from a delayed generate response', async () => {
    let resolve!: (value: typeof result) => void
    vi.mocked(adminAPI.antigravity.generateAuthUrl).mockImplementation(() => new Promise(r => { resolve = r }))
    const oauth = useAntigravityOAuth()
    const pending = oauth.generateAuthUrl(null)
    oauth.resetState()
    resolve(result)
    expect(await pending).toBe(false)
    expect([oauth.authUrl.value, oauth.sessionId.value, oauth.state.value, oauth.loading.value]).toEqual(['', '', '', false])
  })

  it('ignores a code exchange completing after the account dialog was closed', async () => {
    let resolve!: (value: { access_token: string }) => void
    vi.mocked(adminAPI.antigravity.exchangeCode).mockImplementation(() => new Promise(r => { resolve = r }))
    const oauth = useAntigravityOAuth()
    const pending = oauth.exchangeAuthCode({ code: 'fixture', sessionId: result.session_id, state: result.state })
    oauth.resetState()
    resolve({ access_token: 'unused-token' })
    expect(await pending).toBeNull()
    expect(oauth.loading.value).toBe(false)
  })

  it('shows the normalized API error and allows a fresh generation attempt', async () => {
    vi.useFakeTimers()
    vi.mocked(adminAPI.antigravity.generateAuthUrl).mockRejectedValueOnce(new Error('generation unavailable')).mockResolvedValueOnce(result)
    const oauth = useAntigravityOAuth()
    expect(await oauth.generateAuthUrl(null)).toBe(false)
    expect(oauth.error.value).toBe('generation unavailable')
    expect(await oauth.generateAuthUrl(null)).toBe(true)
    expect(oauth.error.value).toBe('')
    expect(oauth.sessionId.value).toBe(result.session_id)
    oauth.resetState()
  })

  it('does not offer an already expired link', async () => {
    vi.mocked(adminAPI.antigravity.generateAuthUrl).mockResolvedValue({ ...result, expires_at: 1 })
    const oauth = useAntigravityOAuth()
    expect(await oauth.generateAuthUrl(null)).toBe(false)
    expect(oauth.authUrl.value).toBe('')
    expect(oauth.error.value).toBe('admin.accounts.antigravityAuthExpired')
  })

  it('keeps the legacy edge response usable for the existing 30-minute TTL', async () => {
    vi.useFakeTimers()
    vi.mocked(adminAPI.antigravity.generateAuthUrl).mockResolvedValue(result)
    const oauth = useAntigravityOAuth()
    expect(await oauth.generateAuthUrl(null)).toBe(true)
    vi.advanceTimersByTime(29 * 60_000)
    expect(oauth.sessionId.value).toBe(result.session_id)
    vi.advanceTimersByTime(60_000)
    expect(oauth.sessionId.value).toBe('')
    expect(oauth.error.value).toBe('admin.accounts.antigravityAuthExpired')
  })
})
