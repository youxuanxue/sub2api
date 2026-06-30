import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import * as studioDownload from '@/utils/studioDownload.tk'
import { useStudioVideoCardActions } from '../useStudioVideoCardActions'

describe('useStudioVideoCardActions', () => {
  beforeEach(() => {
    vi.stubGlobal('navigator', { ...navigator, clipboard: { writeText: vi.fn(async () => undefined) } })
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
  })

  it('copyCardLink writes url to clipboard', async () => {
    const writeText = vi.fn(async () => undefined)
    vi.stubGlobal('navigator', { ...navigator, clipboard: { writeText } })
    const { copyCardLink } = useStudioVideoCardActions()
    await copyCardLink('https://cdn.example/a.mp4')
    expect(writeText).toHaveBeenCalledWith('https://cdn.example/a.mp4')
  })

  it('downloadCardVideo warns instead of downloading when urlExpired', () => {
    const onExpiredDownload = vi.fn()
    const downloadSpy = vi.spyOn(studioDownload, 'downloadMedia')
    const { downloadCardVideo } = useStudioVideoCardActions(onExpiredDownload)
    downloadCardVideo('https://cdn.example/a.mp4', 'tokenkey-vt_1.mp4', true)
    expect(onExpiredDownload).toHaveBeenCalled()
    expect(downloadSpy).not.toHaveBeenCalled()
    downloadSpy.mockRestore()
  })
})
