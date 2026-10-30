import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get } = vi.hoisted(() => ({ get: vi.fn() }))
vi.mock('@/api/client', () => ({ apiClient: { get } }))

import { getAll, getAllWithCount, list } from '@/api/admin/proxies'

describe.each([
  { name: 'paginated list', load: () => list(), wrap: (items: unknown[]) => ({ items, total: items.length, pages: 1 }) },
  { name: 'account selector', load: getAll, wrap: (items: unknown[]) => items },
  { name: 'account selector with counts', load: getAllWithCount, wrap: (items: unknown[]) => items },
])('$name', ({ load, wrap }) => {
  beforeEach(() => { get.mockReset() })

  it.each(['', undefined, null, {}, { items: null }, { items: {} }])(
    'rejects an empty or malformed successful response: %j',
    async (data) => {
      get.mockResolvedValue({ data })
      await expect(load()).rejects.toThrow('Invalid proxy list response')
    },
  )

  it.each([{ items: [] }, { items: [{ id: 1, name: 'valid proxy' }] }])('preserves valid rows: %j', async ({ items }) => {
    const data = wrap(items)
    get.mockResolvedValue({ data })
    await expect(load()).resolves.toBe(data)
  })
})

describe('complete proxy catalog', () => {
  beforeEach(() => { get.mockReset() })

  it('loads every page including inactive and expired proxies without a status filter', async () => {
    const active = { id: 1, name: 'Active', status: 'active' }
    const inactive = { id: 2, name: 'Disabled', status: 'inactive' }
    const expired = { id: 3, name: 'Expired', status: 'expired' }
    get.mockResolvedValueOnce({ data: { items: [active], total: 3 } })
      .mockResolvedValueOnce({ data: { items: [inactive, expired], total: 3 } })
    await expect(getAll({ includeInactive: true })).resolves.toEqual([active, inactive, expired])
    expect(get).toHaveBeenCalledTimes(2)
    for (const page of [1, 2]) {
      expect(get).toHaveBeenNthCalledWith(page, '/admin/proxies', {
        params: { page, page_size: 100, sort_by: 'id', sort_order: 'asc' }, signal: undefined
      })
    }
  })

  it('rejects an incomplete catalog rather than returning partial filter choices', async () => {
    get.mockResolvedValue({ data: { items: [], total: 1 } })
    await expect(getAll({ includeInactive: true })).rejects.toThrow('Incomplete proxy list response')
  })

  it('propagates a later page failure', async () => {
    get.mockResolvedValueOnce({ data: { items: [{ id: 1 }], total: 2 } })
      .mockRejectedValueOnce(new Error('network error'))
    await expect(getAll({ includeInactive: true })).rejects.toThrow('network error')
  })
})
