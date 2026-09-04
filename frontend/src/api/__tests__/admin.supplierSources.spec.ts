import { beforeEach, describe, expect, it, vi } from 'vitest'

const { post } = vi.hoisted(() => ({
  post: vi.fn(),
}))

vi.mock('../client', () => ({
  apiClient: {
    post,
  },
}))

import supplierSources from '@/api/admin/supplierSources'

describe('admin supplierSources API', () => {
  beforeEach(() => {
    post.mockReset()
  })

  it('gives validate a long timeout because it probes every configured model', async () => {
    post.mockResolvedValue({ data: { source_id: 7, probe_results: [] } })

    await supplierSources.validate(7)

    expect(post).toHaveBeenCalledWith(
      '/admin/supplier-sources/7/validate',
      undefined,
      { timeout: 300_000 },
    )
  })

  it('keeps discover and project on the default client timeout', async () => {
    post.mockResolvedValue({ data: { source_id: 7 } })

    await supplierSources.discover(7)
    await supplierSources.sync(7)

    expect(post).toHaveBeenCalledWith('/admin/supplier-sources/7/discover', undefined, undefined)
    expect(post).toHaveBeenCalledWith('/admin/supplier-sources/7/sync')
  })

  it('passes channel_scoped when discover is restricted to channel family', async () => {
    post.mockResolvedValue({ data: { source_id: 7 } })

    await supplierSources.discover(7, { channelScoped: true })

    expect(post).toHaveBeenCalledWith(
      '/admin/supplier-sources/7/discover',
      undefined,
      { params: { channel_scoped: '1' } },
    )
  })
})
