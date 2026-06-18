import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  gatewayImageGenerations,
  gatewayImagePresign,
  gatewayVideoSubmit,
  gatewayGeminiImageViaChat,
  gatewayImageToPrompt,
} from '@/api/playground'

// Capture the JSON body each builder sends so we can assert the wire shape:
// only set fields are sent; video advanced params nest under metadata; never video_url.
function mockFetchCapturing(): () => Record<string, unknown> {
  let body: Record<string, unknown> = {}
  vi.stubGlobal(
    'fetch',
    vi.fn(async (_url: string, init: RequestInit) => {
      body = JSON.parse((init.body as string) || '{}')
      return new Response(JSON.stringify({ ok: true }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    })
  )
  return () => body
}

describe('gatewayImageGenerations payload', () => {
  let getBody: () => Record<string, unknown>
  beforeEach(() => {
    getBody = mockFetchCapturing()
  })
  afterEach(() => vi.unstubAllGlobals())

  it('sends only model + prompt when nothing optional is set', async () => {
    await gatewayImageGenerations('k', 'http://x', { model: 'imagen-4.0-fast-generate-001', prompt: 'hi' })
    expect(getBody()).toEqual({ model: 'imagen-4.0-fast-generate-001', prompt: 'hi' })
  })

  it('sends size + n when provided (imagen/seedream honor no other params)', async () => {
    await gatewayImageGenerations('k', 'http://x', { model: 'm', prompt: 'hi', size: '1024x1024', n: 2 })
    expect(getBody()).toEqual({ model: 'm', prompt: 'hi', size: '1024x1024', n: 2 })
  })
})

describe('gatewayImagePresign', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('POSTs the key and returns the re-minted url', async () => {
    let sentUrl = ''
    let sentBody: Record<string, unknown> = {}
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string, init: RequestInit) => {
        sentUrl = url
        sentBody = JSON.parse((init.body as string) || '{}')
        return new Response(JSON.stringify({ url: 'https://s3.example/media/images/abc.png?sig=fresh' }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        })
      })
    )
    const url = await gatewayImagePresign('k', 'http://x/', 'media/images/abc.png')
    expect(sentUrl).toBe('http://x/v1/images/presign')
    expect(sentBody).toEqual({ key: 'media/images/abc.png' })
    expect(url).toBe('https://s3.example/media/images/abc.png?sig=fresh')
  })

  it('returns empty string when the response carries no url', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () =>
        new Response(JSON.stringify({}), { status: 200, headers: { 'Content-Type': 'application/json' } })
      )
    )
    expect(await gatewayImagePresign('k', 'http://x', 'media/images/abc.png')).toBe('')
  })
})

describe('gatewayVideoSubmit payload', () => {
  let getBody: () => Record<string, unknown>
  beforeEach(() => {
    getBody = mockFetchCapturing()
  })
  afterEach(() => vi.unstubAllGlobals())

  it('sends only model + prompt when nothing optional is set (no empty metadata)', async () => {
    await gatewayVideoSubmit('k', 'http://x', { model: 'veo-3.1-generate-001', prompt: 'hi' })
    expect(getBody()).toEqual({ model: 'veo-3.1-generate-001', prompt: 'hi' })
  })

  it('nests seed/negativePrompt under metadata, image stays top-level', async () => {
    await gatewayVideoSubmit('k', 'http://x', {
      model: 'veo-3.1-generate-001',
      prompt: 'hi',
      duration: 8,
      aspectRatio: '16:9',
      seed: 42,
      negativePrompt: 'shaky',
      image: 'https://cdn/x.png',
    })
    const b = getBody()
    expect(b).toMatchObject({
      model: 'veo-3.1-generate-001',
      prompt: 'hi',
      duration: 8,
      aspect_ratio: '16:9',
      image: 'https://cdn/x.png',
    })
    expect(b.metadata).toEqual({ seed: 42, negative_prompt: 'shaky' })
    // never forward video input — backend rejects it as unpriced
    expect(JSON.stringify(b)).not.toContain('video_url')
  })
})

describe('gatewayGeminiImageViaChat payload (image-to-image)', () => {
  let getBody: () => Record<string, unknown>
  beforeEach(() => {
    getBody = mockFetchCapturing()
  })
  afterEach(() => vi.unstubAllGlobals())

  it('sends plain string content when no input image (text-to-image)', async () => {
    await gatewayGeminiImageViaChat('k', 'http://x', { model: 'gemini-3.1-flash-image', prompt: 'a cat' })
    const msgs = getBody().messages as Array<{ role: string; content: unknown }>
    expect(msgs[0]).toEqual({ role: 'user', content: 'a cat' })
  })

  it('sends multimodal [text, image_url] content when an input image is staged', async () => {
    await gatewayGeminiImageViaChat('k', 'http://x', {
      model: 'gemini-3.1-flash-image',
      prompt: 'make it blue',
      inputImage: 'data:image/png;base64,AAAA',
    })
    const msgs = getBody().messages as Array<{ role: string; content: unknown }>
    expect(msgs[0]).toEqual({
      role: 'user',
      content: [
        { type: 'text', text: 'make it blue' },
        { type: 'image_url', image_url: { url: 'data:image/png;base64,AAAA' } },
      ],
    })
  })
})

describe('gatewayImageToPrompt', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('sends the image as multimodal content and returns the assistant text', async () => {
    let body: Record<string, unknown> = {}
    vi.stubGlobal(
      'fetch',
      vi.fn(async (_url: string, init: RequestInit) => {
        body = JSON.parse((init.body as string) || '{}')
        return new Response(
          JSON.stringify({ choices: [{ message: { content: '  a red square on white  ' } }] }),
          { status: 200, headers: { 'Content-Type': 'application/json' } }
        )
      })
    )
    const text = await gatewayImageToPrompt('k', 'http://x', {
      model: 'gemini-2.5-flash-lite',
      image: 'data:image/png;base64,BBBB',
    })
    expect(text).toBe('a red square on white')
    const msgs = body.messages as Array<{ content: Array<{ type: string }> }>
    expect(msgs[0].content.map((p) => p.type)).toEqual(['text', 'image_url'])
  })
})
