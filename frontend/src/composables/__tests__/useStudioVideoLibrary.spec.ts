import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mountStudioVideoLibrary } from '../useStudioVideoLibrary'

describe('useStudioVideoLibrary', () => {
  beforeEach(() => vi.clearAllMocks())

  it('mountStudioVideoLibrary hydrates mirrored clips from IndexedDB', async () => {
    const hydrate = vi.fn(async () => undefined)
    await mountStudioVideoLibrary({
      hydrateFromBlobCache: hydrate,
    })
    expect(hydrate).toHaveBeenCalled()
  })
})
