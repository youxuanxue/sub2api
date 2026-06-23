import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { AxiosInstance } from 'axios'

vi.mock('@/i18n', () => ({
  getLocale: () => 'zh-CN',
}))

describe('admin edgeAccounts API', () => {
  let apiClient: AxiosInstance
  let edgeAccounts: typeof import('@/api/admin/edgeAccounts')

  beforeEach(async () => {
    vi.resetModules()
    const clientMod = await import('@/api/client')
    apiClient = clientMod.apiClient
    edgeAccounts = await import('@/api/admin/edgeAccounts')
  })

  it('sends If-None-Match for passive ETag refreshes', async () => {
    const adapter = vi.fn().mockResolvedValue({
      status: 304,
      data: null,
      headers: { etag: '"edge-etag"' },
      config: {},
      statusText: 'Not Modified',
    })
    apiClient.defaults.adapter = adapter

    await expect(edgeAccounts.listWithEtag({ view: 'by-stub' }, { etag: '"old"' })).resolves.toEqual({
      notModified: true,
      etag: '"edge-etag"',
      data: null,
    })

    const config = adapter.mock.calls[0][0]
    expect(config.params).toMatchObject({ view: 'by-stub' })
    expect(config.params.force).toBeUndefined()
    expect(config.headers.get('If-None-Match')).toBe('"old"')
  })

  it('force refresh bypasses the client ETag and asks the backend for a fresh fan-out', async () => {
    const adapter = vi.fn().mockResolvedValue({
      status: 200,
      data: { code: 0, data: { platform: '__by_stub__', edges: [], ts: 1 }, message: 'ok' },
      headers: { etag: '"fresh"' },
      config: {},
      statusText: 'OK',
    })
    apiClient.defaults.adapter = adapter

    await expect(edgeAccounts.listWithEtag({ view: 'by-stub' }, { etag: '"old"', force: true })).resolves.toEqual({
      notModified: false,
      etag: '"fresh"',
      data: { platform: '__by_stub__', edges: [], ts: 1 },
    })

    const config = adapter.mock.calls[0][0]
    expect(config.params).toMatchObject({ view: 'by-stub', force: 'true' })
    expect(config.headers.get('If-None-Match')).toBeFalsy()
  })
})
