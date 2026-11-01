import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const { post } = vi.hoisted(() => ({ post: vi.fn() }))

vi.mock('../client', () => ({ apiClient: { post } }))

import { rotate } from '../keys'

describe('API key rotation API', () => {
  beforeEach(() => {
    post.mockReset()
    post.mockResolvedValue({ data: { id: 7, key: 'sk-new' } })
    vi.spyOn(globalThis.crypto, 'randomUUID').mockReturnValue('11111111-1111-4111-8111-111111111111')
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('sends an idempotency key required by the rotate endpoint', async () => {
    await expect(rotate(7)).resolves.toEqual({ id: 7, key: 'sk-new' })

    expect(post).toHaveBeenCalledWith('/keys/7/rotate', undefined, {
      headers: {
        'Idempotency-Key': 'api-key-rotate-7-11111111-1111-4111-8111-111111111111'
      }
    })
  })
})
