import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import { useStudioVideoPreview } from '../useStudioVideoPreview'

describe('useStudioVideoPreview', () => {
  beforeEach(() => {
    vi.stubGlobal(
      'URL',
      Object.assign({}, URL, {
        createObjectURL: vi.fn(() => 'blob:preview'),
        revokeObjectURL: vi.fn(),
      })
    )
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('converts inline data:video to blob playback url', () => {
    const preview = useStudioVideoPreview()
    preview.openPreview({
      url: 'data:video/mp4;base64,QUFB',
      label: 'Veo 3.1',
      cost: 4.8,
      taskId: 'vt_blob',
    })
    expect(URL.createObjectURL).toHaveBeenCalled()
    expect(preview.previewUrl.value).toBe('blob:preview')
    expect(preview.previewState.value).toBe('ready')
  })

  it('calls onUrlExpired with task id after preview error', () => {
    const onUrlExpired = vi.fn()
    const preview = useStudioVideoPreview({ onUrlExpired })
    preview.openPreview({
      url: 'https://cdn.example/clip.mp4',
      label: 'Seedance',
      cost: 1,
      taskId: 'vt_err',
    })
    preview.onPreviewError()
    expect(onUrlExpired).toHaveBeenCalledWith('vt_err')
    expect(preview.open.value).toBe(false)
  })
})
