import { describe, expect, it } from 'vitest'
import { useStudioImagePreview } from '../useStudioImagePreview'
import type { ImageHistoryItem } from '../useMediaLibrary'

function row(src: string): ImageHistoryItem {
  return {
    id: '1',
    src,
    prompt: 'p',
    model: 'imagen-4',
    vendorLabel: 'Vertex',
    size: '1:1',
    cost: 0.04,
    ts: 1,
  }
}

describe('useStudioImagePreview', () => {
  it('opens only when src is available', () => {
    const { preview, openPreview, closePreview } = useStudioImagePreview()
    openPreview(row(''))
    expect(preview.value).toBeNull()
    openPreview(row('data:image/png;base64,AA=='))
    expect(preview.value?.src).toContain('data:image')
    closePreview()
    expect(preview.value).toBeNull()
  })
})
