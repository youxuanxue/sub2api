import { ref } from 'vue'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import {
  mountStudioImageLibrary,
  onStudioImageThumbError,
  refreshStudioOffloadedImageUrls,
} from '../useStudioImageLibrary'
import type { ImageHistoryItem } from '../useMediaLibrary'

vi.mock('@/api/playground', () => ({
  gatewayImagePresign: vi.fn(async (_key: string, _base: string, s3Key: string) =>
    s3Key ? `https://fresh.example/${s3Key}` : ''
  ),
}))

function imageRow(id: string, s3Key?: string): ImageHistoryItem {
  return {
    id,
    src: '',
    s3Key,
    prompt: 'cat',
    model: 'imagen-4',
    vendorLabel: 'Vertex',
    size: '1:1',
    cost: 0.04,
    ts: 1,
  }
}

describe('useStudioImageLibrary', () => {
  beforeEach(() => vi.clearAllMocks())

  it('refreshStudioOffloadedImageUrls re-presigns s3Key rows', async () => {
    const rows = ref([imageRow('a', 'media/a.png')])
    await refreshStudioOffloadedImageUrls('key', 'https://gw', rows)
    expect(rows.value[0].src).toBe('https://fresh.example/media/a.png')
  })

  it('mountStudioImageLibrary hydrates then presigns', async () => {
    const images = ref([imageRow('b', 'media/b.png')])
    const hydrate = vi.fn(async () => {
      images.value[0].src = 'blob:cached'
    })
    const library = {
      images,
      hydrateFromBlobCache: hydrate,
      rehydrateImageFromBlob: vi.fn(async () => true),
    }
    await mountStudioImageLibrary('key', 'https://gw', library)
    expect(hydrate).toHaveBeenCalled()
    expect(images.value[0].src).toBe('https://fresh.example/media/b.png')
  })

  it('onStudioImageThumbError delegates to rehydrateImageFromBlob', async () => {
    const rehydrate = vi.fn(async () => true)
    await onStudioImageThumbError(
      { images: ref([]), hydrateFromBlobCache: vi.fn(), rehydrateImageFromBlob: rehydrate },
      'id-1'
    )
    expect(rehydrate).toHaveBeenCalledWith('id-1')
  })
})
