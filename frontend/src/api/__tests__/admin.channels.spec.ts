import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import type { AxiosInstance } from 'axios'

vi.mock('@/i18n', () => ({
  getLocale: () => 'zh-CN',
}))

describe('admin channels API', () => {
  let apiClient: AxiosInstance
  let channels: typeof import('@/api/admin/channels')

  beforeEach(async () => {
    vi.resetModules()
    const clientMod = await import('@/api/client')
    apiClient = clientMod.apiClient
    channels = await import('@/api/admin/channels')
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('normalizes #128 upstream model objects and preserves pricing status', async () => {
    apiClient.defaults.adapter = vi.fn().mockResolvedValue({
      status: 200,
      data: {
        code: 0,
        data: {
          models: [
            { id: 'claude-opus-4-6', pricing_status: 'priced' },
            { id: 'claude-sonnet-4-6', pricing_status: 'missing' },
            { id: 'claude-opus-4-6', pricing_status: 'priced' },
            '',
          ],
        },
        message: 'ok',
      },
      headers: {},
      config: {},
      statusText: 'OK',
    })

    await expect(channels.fetchUpstreamModels({
      base_url: 'https://example.com',
      channel_type: 1,
      api_key: 'sk-test',
    })).resolves.toEqual([
      { id: 'claude-opus-4-6', pricing_status: 'priced' },
      { id: 'claude-sonnet-4-6', pricing_status: 'missing' },
    ])
  })
})
