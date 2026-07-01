import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import * as studioDownload from '@/utils/studioDownload.tk'
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

  it('converts inline data:video to blob playback url', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => ({
        blob: async () => new Blob([new Uint8Array([1, 2, 3])], { type: 'video/mp4' }),
      }))
    )
    const preview = useStudioVideoPreview()
    preview.openPreview({
      url: 'data:video/mp4;base64,QUFB',
      label: 'Veo 3.1',
      cost: 4.8,
      taskId: 'vt_blob',
    })
    await vi.waitFor(() => {
      expect(preview.previewState.value).toBe('ready')
    })
    expect(URL.createObjectURL).toHaveBeenCalled()
    expect(preview.previewUrl.value).toBe('blob:preview')
  })

  it('discards async playback when preview closes before decode finishes', async () => {
    let resolveBlob: (value: Blob) => void = () => {}
    vi.stubGlobal(
      'fetch',
      vi.fn(
        () =>
          new Promise<Response>((resolve) => {
            resolve({
              blob: () =>
                new Promise<Blob>((r) => {
                  resolveBlob = r
                }),
            } as Response)
          })
      )
    )
    const preview = useStudioVideoPreview()
    preview.openPreview({
      url: 'data:video/mp4;base64,QUFB',
      label: 'Veo 3.1',
      cost: 4.8,
      taskId: 'vt_slow',
    })
    preview.closePreview()
    resolveBlob(new Blob([new Uint8Array([1])], { type: 'video/mp4' }))
    await Promise.resolve()
    expect(preview.open.value).toBe(false)
    expect(preview.previewUrl.value).toBe('')
  })

  it('marks lightbox expired after preview error without mutating library tasks', () => {
    const preview = useStudioVideoPreview()
    preview.openPreview({
      url: 'https://cdn.example/clip.mp4',
      label: 'Seedance',
      cost: 1,
      taskId: 'vt_err',
    })
    preview.onPreviewError()
    expect(preview.open.value).toBe(true)
    expect(preview.previewState.value).toBe('expired')
    expect(preview.urlExpired.value).toBe(true)
    expect(preview.rawUrl.value).toBe('https://cdn.example/clip.mp4')
  })

  it('copyPreviewLink downloads inline Veo clips instead of copying data: URI', async () => {
    const writeText = vi.fn(async () => undefined)
    const onInlineCopyUnsupported = vi.fn()
    const downloadSpy = vi.spyOn(studioDownload, 'downloadMedia')
    vi.stubGlobal('navigator', { ...navigator, clipboard: { writeText } })
    const preview = useStudioVideoPreview({ onInlineCopyUnsupported })
    preview.openPreview({
      url: 'data:video/mp4;base64,QUFB',
      label: 'Veo 3.1',
      cost: 4.8,
      taskId: 'vt_blob',
    })
    await preview.copyPreviewLink()
    expect(writeText).not.toHaveBeenCalled()
    expect(onInlineCopyUnsupported).toHaveBeenCalled()
    expect(downloadSpy).toHaveBeenCalledWith('data:video/mp4;base64,QUFB', 'tokenkey-preview.mp4')
    downloadSpy.mockRestore()
    vi.unstubAllGlobals()
  })

  it('downloadPreview still uses raw upstream url after lightbox playback fails', () => {
    const onExpiredDownload = vi.fn()
    const downloadSpy = vi.spyOn(studioDownload, 'downloadMedia')
    const preview = useStudioVideoPreview({ onExpiredDownload })
    preview.openPreview({
      url: 'https://cdn.example/clip.mp4',
      label: 'Seedance',
      cost: 1,
      taskId: 'vt_err',
    })
    preview.onPreviewError()
    preview.downloadPreview()
    expect(downloadSpy).toHaveBeenCalledWith('https://cdn.example/clip.mp4', 'tokenkey-preview.mp4')
    expect(onExpiredDownload).not.toHaveBeenCalled()
    downloadSpy.mockRestore()
  })

  it('downloadPreview invokes onExpiredDownload when no raw url is available', () => {
    const onExpiredDownload = vi.fn()
    const downloadSpy = vi.spyOn(studioDownload, 'downloadMedia')
    const preview = useStudioVideoPreview({ onExpiredDownload })
    preview.downloadPreview()
    expect(onExpiredDownload).toHaveBeenCalled()
    expect(downloadSpy).not.toHaveBeenCalled()
    downloadSpy.mockRestore()
  })
})
