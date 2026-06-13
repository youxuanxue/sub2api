import { describe, expect, it, vi, beforeEach } from 'vitest'
import { getModelsListCandidates } from '@/api/admin/groups'

vi.mock('@/api/admin/groups', () => ({
  getModelsListCandidates: vi.fn()
}))

import { useServableModels, servableModelsFor, isApiBackedPlatform } from '../useServableModels'

const mockGet = vi.mocked(getModelsListCandidates)

describe('useServableModels', () => {
  beforeEach(() => {
    mockGet.mockReset()
  })

  it('isApiBackedPlatform covers the 4 self-healing platforms only', () => {
    for (const p of ['anthropic', 'claude', 'openai', 'gemini', 'antigravity']) {
      expect(isApiBackedPlatform(p)).toBe(true)
    }
    for (const p of ['newapi', 'zhipu', 'kiro', 'totally-unknown']) {
      expect(isApiBackedPlatform(p)).toBe(false)
    }
  })

  it('ensureLoaded fetches the self-healing list with id=0 and caches it (claude→anthropic)', async () => {
    mockGet.mockResolvedValueOnce(['claude-opus-4-8', 'claude-sonnet-4-6'])
    const { ensureLoaded } = useServableModels()

    await ensureLoaded('claude')

    expect(mockGet).toHaveBeenCalledWith(0, 'anthropic')
    expect(servableModelsFor('claude')).toEqual(['claude-opus-4-8', 'claude-sonnet-4-6'])
    // cached: a second ensureLoaded does not refetch
    await ensureLoaded('anthropic')
    expect(mockGet).toHaveBeenCalledTimes(1)
  })

  it('a fetch error degrades to an empty cache + surfaced error (no crash)', async () => {
    mockGet.mockRejectedValueOnce(new Error('boom'))
    const { ensureLoaded, error } = useServableModels()

    await ensureLoaded('openai')

    expect(servableModelsFor('openai')).toEqual([])
    expect(error.value).toContain('boom')
  })

  it('non-API platform is a no-op (no fetch, undefined list → caller uses its static fallback)', async () => {
    const { ensureLoaded } = useServableModels()
    await ensureLoaded('zhipu')
    expect(mockGet).not.toHaveBeenCalled()
    expect(servableModelsFor('zhipu')).toBeUndefined()
  })
})
