import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import type { AxiosInstance, AxiosRequestConfig } from 'axios'

vi.mock('@/i18n', () => ({
  getLocale: () => 'zh-CN'
}))

describe('qaTraj API', () => {
  let apiClient: AxiosInstance
  let qaTraj: typeof import('@/api/qaTraj')

  beforeEach(async () => {
    vi.resetModules()
    const clientMod = await import('@/api/client')
    apiClient = clientMod.apiClient
    qaTraj = await import('@/api/qaTraj')
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('exportKey posts the per-key v2 payload and unwraps the envelope', async () => {
    let captured: AxiosRequestConfig | undefined
    apiClient.defaults.adapter = vi.fn().mockImplementation((config: AxiosRequestConfig) => {
      captured = config
      return Promise.resolve({
        status: 200,
        statusText: 'OK',
        headers: {},
        config,
        data: {
          code: 0,
          data: { download_url: '/api/v1/users/me/qa/traj/exports/k.zip', expires_at: 't', record_count: 3 }
        }
      })
    })

    const result = await qaTraj.qaTrajAPI.exportKey(42)

    expect(captured?.url).toBe('/users/me/qa/traj/export')
    expect(captured?.method?.toLowerCase()).toBe('post')
    expect(JSON.parse(captured?.data as string)).toEqual({ api_key_id: 42, format: 'v2' })
    expect(result.record_count).toBe(3)
    expect(result.download_url).toBe('/api/v1/users/me/qa/traj/exports/k.zip')
  })
})
