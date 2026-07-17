import { describe, expect, it, vi, afterEach } from 'vitest'
import * as studioDownload from '@/utils/studioDownload.tk'
import { studioImageDownloadFilename, useStudioImageCardActions } from '../useStudioImageCardActions'

describe('useStudioImageCardActions', () => {
  afterEach(() => {
    vi.restoreAllMocks()
    vi.useRealTimers()
  })

  it('studioImageDownloadFilename keeps the canonical tokenkey-<id>.png format', () => {
    expect(studioImageDownloadFilename('img_1')).toBe('tokenkey-img_1.png')
  })

  it('downloadCardImage delegates to the studioDownload.tk owner with the canonical filename', () => {
    const downloadSpy = vi.spyOn(studioDownload, 'downloadMedia').mockImplementation(() => undefined)
    const { downloadCardImage } = useStudioImageCardActions()
    downloadCardImage({ id: 'img_1', src: 'data:image/png;base64,AAAA' })
    expect(downloadSpy).toHaveBeenCalledWith('data:image/png;base64,AAAA', 'tokenkey-img_1.png')
  })

  it('downloadAllImages staggers each save by 350ms in list order', () => {
    vi.useFakeTimers()
    const downloadSpy = vi.spyOn(studioDownload, 'downloadMedia').mockImplementation(() => undefined)
    const { downloadAllImages } = useStudioImageCardActions()
    downloadAllImages([
      { id: 'a', src: 'data:image/png;base64,AAAA' },
      { id: 'b', src: 'data:image/png;base64,BBBB' },
    ])
    // First save fires at t=0, second at t=350 — not before.
    vi.advanceTimersByTime(0)
    expect(downloadSpy).toHaveBeenCalledTimes(1)
    expect(downloadSpy).toHaveBeenNthCalledWith(1, 'data:image/png;base64,AAAA', 'tokenkey-a.png')
    vi.advanceTimersByTime(349)
    expect(downloadSpy).toHaveBeenCalledTimes(1)
    vi.advanceTimersByTime(1)
    expect(downloadSpy).toHaveBeenCalledTimes(2)
    expect(downloadSpy).toHaveBeenNthCalledWith(2, 'data:image/png;base64,BBBB', 'tokenkey-b.png')
  })

  it('downloadAllImages with an expired (empty-src) row still delegates; owner downloadMedia no-ops on empty url', () => {
    vi.useFakeTimers()
    const downloadSpy = vi.spyOn(studioDownload, 'downloadMedia')
    const clickSpy = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => undefined)
    const { downloadAllImages } = useStudioImageCardActions()
    downloadAllImages([{ id: 'expired', src: '' }])
    vi.advanceTimersByTime(0)
    // Same as the pre-composable behavior: the empty-src guard lives in
    // downloadMedia (returns before creating an anchor), not in the view.
    expect(downloadSpy).toHaveBeenCalledWith('', 'tokenkey-expired.png')
    expect(clickSpy).not.toHaveBeenCalled()
  })
})
