import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { downloadMedia } from '@/utils/studioDownload.tk'

describe('downloadMedia', () => {
  let clickSpy: ReturnType<typeof vi.fn>
  let lastAnchor: HTMLAnchorElement | null

  beforeEach(() => {
    lastAnchor = null
    clickSpy = vi.fn()
    const realCreate = document.createElement.bind(document)
    vi.spyOn(document, 'createElement').mockImplementation((tag: string) => {
      const el = realCreate(tag) as HTMLElement
      if (tag === 'a') {
        lastAnchor = el as HTMLAnchorElement
        ;(el as HTMLAnchorElement).click = clickSpy
      }
      return el
    })
  })
  afterEach(() => vi.restoreAllMocks())

  it('no-ops on empty url', () => {
    downloadMedia('', 'x.png')
    expect(clickSpy).not.toHaveBeenCalled()
  })

  it('downloads a data: URL with the filename and no new tab', () => {
    downloadMedia('data:image/png;base64,AAAA', 'tokenkey-1.png')
    expect(clickSpy).toHaveBeenCalledOnce()
    expect(lastAnchor?.getAttribute('download')).toBe('tokenkey-1.png')
    expect(lastAnchor?.target).toBe('')
  })

  it('downloads a blob: URL with the filename and no new tab', () => {
    downloadMedia('blob:https://app.example/video-object', 'tokenkey-vt_1.mp4')
    expect(clickSpy).toHaveBeenCalledOnce()
    expect(lastAnchor?.getAttribute('download')).toBe('tokenkey-vt_1.mp4')
    expect(lastAnchor?.target).toBe('')
  })

  it('opens a remote URL in a new tab (download attr is cross-origin-ignored)', () => {
    downloadMedia('https://cdn.example.com/v.mp4', 'tokenkey-vt_1.mp4')
    expect(clickSpy).toHaveBeenCalledOnce()
    expect(lastAnchor?.getAttribute('download')).toBe('tokenkey-vt_1.mp4')
    expect(lastAnchor?.target).toBe('_blank')
    expect(lastAnchor?.rel).toBe('noopener')
  })

  it('falls back to window.open when anchor click throws', () => {
    const openSpy = vi.spyOn(window, 'open').mockImplementation(() => null)
    clickSpy.mockImplementation(() => {
      throw new Error('blocked')
    })
    downloadMedia('https://cdn.example.com/v.mp4', 'x.mp4')
    expect(openSpy).toHaveBeenCalledWith('https://cdn.example.com/v.mp4', '_blank')
  })
})

describe('copyMediaLink', () => {
  it('returns false for empty url', async () => {
    const { copyMediaLink } = await import('@/utils/studioDownload.tk')
    await expect(copyMediaLink('')).resolves.toBe(false)
  })

  it('writes upstream url to clipboard', async () => {
    const writeText = vi.fn(async () => undefined)
    vi.stubGlobal('navigator', { ...navigator, clipboard: { writeText } })
    const { copyMediaLink } = await import('@/utils/studioDownload.tk')
    await expect(copyMediaLink('https://cdn.example/v.mp4')).resolves.toBe(true)
    expect(writeText).toHaveBeenCalledWith('https://cdn.example/v.mp4')
    vi.unstubAllGlobals()
  })
})
