import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { gatewayImageGenerations, gatewayVideoSubmit } from '@/api/playground'

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
