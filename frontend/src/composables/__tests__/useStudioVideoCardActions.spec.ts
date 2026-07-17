import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import * as studioDownload from '@/utils/studioDownload.tk'
import { createStudioVideoActionHandlers, useStudioVideoCardActions } from '../useStudioVideoCardActions'

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
    const { downloadCardVideo } = useStudioVideoCardActions({ onExpiredDownload })
    downloadCardVideo('https://cdn.example/a.mp4', 'tokenkey-vt_1.mp4', true)
    expect(onExpiredDownload).toHaveBeenCalled()
    expect(downloadSpy).not.toHaveBeenCalled()
    downloadSpy.mockRestore()
  })

  it('copyCardLink triggers download for inline Veo clips instead of clipboard', async () => {
    const writeText = vi.fn(async () => undefined)
    const onInlineCopyUnsupported = vi.fn()
    const downloadSpy = vi.spyOn(studioDownload, 'downloadMedia')
    vi.stubGlobal('navigator', { ...navigator, clipboard: { writeText } })
    const { copyCardLink } = useStudioVideoCardActions({ onInlineCopyUnsupported })
    await copyCardLink('data:video/mp4;base64,QUJD', 'tokenkey-vt_veo.mp4')
    expect(writeText).not.toHaveBeenCalled()
    expect(onInlineCopyUnsupported).toHaveBeenCalled()
    expect(downloadSpy).toHaveBeenCalledWith('data:video/mp4;base64,QUJD', 'tokenkey-vt_veo.mp4')
    downloadSpy.mockRestore()
  })

  it('createStudioVideoActionHandlers wires expired + inline copy toasts', () => {
    const showWarning = vi.fn()
    const showInfo = vi.fn()
    const t = (key: string) => key
    const handlers = createStudioVideoActionHandlers({ showWarning, showInfo }, t)
    handlers.onExpiredDownload?.()
    handlers.onInlineCopyUnsupported?.()
    expect(showWarning).toHaveBeenCalledWith('studio.video.expiredHint', 8000)
    expect(showInfo).toHaveBeenCalledWith('studio.video.inlineCopyHint', 5000)
  })
})
