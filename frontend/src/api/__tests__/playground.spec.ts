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

  it('includes advanced fields only when provided', async () => {
    await gatewayImageGenerations('k', 'http://x', {
      model: 'm',
      prompt: 'hi',
      size: '1024x1024',
      n: 2,
      seed: 7,
      negative_prompt: 'blurry',
    })
    const b = getBody()
    expect(b).toMatchObject({ model: 'm', prompt: 'hi', size: '1024x1024', n: 2, seed: 7, negative_prompt: 'blurry' })
    expect('quality' in b).toBe(false)
    expect('style' in b).toBe(false)
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

  it('nests seed/negativePrompt/fps under metadata, image stays top-level', async () => {
    await gatewayVideoSubmit('k', 'http://x', {
      model: 'seedance-1-0-pro-250528',
      prompt: 'hi',
      duration: 8,
      aspectRatio: '16:9',
      seed: 42,
      negativePrompt: 'shaky',
      fps: 24,
      image: 'https://cdn/x.png',
    })
    const b = getBody()
    expect(b).toMatchObject({
      model: 'seedance-1-0-pro-250528',
      prompt: 'hi',
      duration: 8,
      aspect_ratio: '16:9',
      image: 'https://cdn/x.png',
    })
    expect(b.metadata).toEqual({ seed: 42, negative_prompt: 'shaky', fps: 24 })
    // never forward video input — backend rejects it as unpriced
    expect(JSON.stringify(b)).not.toContain('video_url')
  })
})
