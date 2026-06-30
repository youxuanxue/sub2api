import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mountStudioVideoLibrary, onStudioVideoReplayError } from '../useStudioVideoLibrary'

describe('useStudioVideoLibrary', () => {
  beforeEach(() => vi.clearAllMocks())

  it('mountStudioVideoLibrary hydrates mirrored clips from IndexedDB', async () => {
    const hydrate = vi.fn(async () => undefined)
    await mountStudioVideoLibrary({
      hydrateFromBlobCache: hydrate,
      rehydrateVideoFromBlob: vi.fn(async () => true),
    })
    expect(hydrate).toHaveBeenCalled()
  })

  it('onStudioVideoReplayError delegates to rehydrateVideoFromBlob', async () => {
    const rehydrate = vi.fn(async () => true)
    await onStudioVideoReplayError(
      { hydrateFromBlobCache: vi.fn(), rehydrateVideoFromBlob: rehydrate },
      'vt-1'
    )
    expect(rehydrate).toHaveBeenCalledWith('vt-1')
  })
})
