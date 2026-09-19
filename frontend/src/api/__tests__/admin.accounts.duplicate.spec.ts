import { beforeEach, describe, expect, it, vi } from 'vitest'

const { post } = vi.hoisted(() => ({
  post: vi.fn()
}))

vi.mock('@/api/client', () => ({
  apiClient: { post }
}))

import { duplicate, duplicateMany } from '@/api/admin/accounts'

describe('admin account duplicate API', () => {
  beforeEach(() => {
    sessionStorage.clear()
    post.mockReset()
    post.mockResolvedValue({ data: { id: 43, name: 'primary (Copy)' } })
    vi.spyOn(globalThis.crypto, 'randomUUID').mockReturnValue('11111111-1111-4111-8111-111111111111')
  })

  it('sends a stable idempotency key with the duplicate request', async () => {
    const account = await duplicate(42)

    expect(post).toHaveBeenCalledWith('/admin/accounts/42/duplicate', undefined, {
      headers: {
        'Idempotency-Key': 'account-duplicate-42-11111111-1111-4111-8111-111111111111'
      }
    })
    expect(account).toEqual({ id: 43, name: 'primary (Copy)' })
  })

  it('reuses the operation key after an ambiguous failed request', async () => {
    post.mockRejectedValueOnce(new Error('network timeout'))
    await expect(duplicate(99)).rejects.toThrow('network timeout')

    post.mockResolvedValueOnce({ data: { id: 100, name: 'retry (Copy)' } })
    await duplicate(99)

    expect(post).toHaveBeenCalledTimes(2)
    const firstHeaders = post.mock.calls[0][2].headers
    const secondHeaders = post.mock.calls[1][2].headers
    expect(secondHeaders).toEqual(firstHeaders)
  })

  it('reuses the operation key after a page reload', async () => {
    post.mockRejectedValueOnce(new Error('network timeout'))
    await expect(duplicate(77)).rejects.toThrow('network timeout')
    const firstHeaders = post.mock.calls[0][2].headers

    vi.resetModules()
    post.mockResolvedValueOnce({ data: { id: 78, name: 'reload (Copy)' } })
    const { duplicate: duplicateAfterReload } = await import('@/api/admin/accounts')
    await duplicateAfterReload(77)

    expect(post).toHaveBeenCalledTimes(2)
    expect(post.mock.calls[1][2].headers).toEqual(firstHeaders)
    expect(sessionStorage.length).toBe(0)
  })
})

describe('batch duplication', () => {
 it('validates count before sending any request', async () => {
  post.mockClear()
  for (const count of [0, -1, 1.5, 101, NaN]) await expect(duplicateMany(42, count)).rejects.toThrow('Copy count')
  expect(post).not.toHaveBeenCalled()
 })
 it('preserves batch count and retry key after ambiguous errors', async () => {
  post.mockClear()
  post.mockRejectedValueOnce(new Error('timeout'))
  await expect(duplicateMany(12, 3)).rejects.toThrow('timeout')
  const headers = post.mock.calls[0][2]
  const copies = [{id: 20, name:'a-1'}, {id:21,name:'a-2'}, {id:22,name:'a-3'}]
  post.mockResolvedValueOnce({data:copies})
  expect(await duplicateMany(12, 3)).toEqual(copies)
  expect(post).toHaveBeenLastCalledWith('/admin/accounts/12/duplicate', {count:3}, headers)
 })
})
