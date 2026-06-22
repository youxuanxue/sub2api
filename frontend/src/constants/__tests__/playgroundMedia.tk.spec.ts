import { describe, expect, it } from 'vitest'
import {
  extractChatImageItems,
  extractImageItems,
  extractVideoTaskId,
  extractVideoUrl,
  isGeminiNativeImageModel,
  modalityForModel,
  pickVisionChatModel,
  videoStateFromFetch
} from '@/constants/playgroundMedia.tk'

describe('pickVisionChatModel', () => {
  it('prefers the cheapest gemini chat model and excludes image/video ids', () => {
    const ids = new Set(['gemini-3.1-flash-image', 'gemini-2.5-flash', 'gemini-2.5-flash-lite', 'gemini-3-pro', 'gpt-4o'])
    expect(pickVisionChatModel(ids)).toBe('gemini-2.5-flash-lite')
  })
  it('falls back through flash then pro', () => {
    expect(pickVisionChatModel(['gemini-3-pro', 'gemini-2.0-flash'])).toBe('gemini-2.0-flash')
    expect(pickVisionChatModel(['gemini-3-pro'])).toBe('gemini-3-pro')
  })
  it('returns empty when no gemini chat model is available', () => {
    expect(pickVisionChatModel(['gpt-4o', 'imagen-4.0-generate-001', 'gemini-3.1-flash-image'])).toBe('')
    expect(pickVisionChatModel([])).toBe('')
  })
})

describe('modalityForModel', () => {
  it('classifies the served image families', () => {
    for (const id of ['gpt-image-1', 'gpt-image-2-2026-04-21', 'imagen-4.0-generate-001', 'doubao-seedream-4-0-250828']) {
      expect(modalityForModel(id)).toBe('image')
    }
  })

  it('classifies gemini-native image models as image (served via chat)', () => {
    for (const id of [
      'gemini-3.1-flash-image',
      'gemini-3.1-flash-image-preview',
      'gemini-2.5-flash-image',
      'gemini-3-pro-image-preview',
      'nano-banana-pro-preview'
    ]) {
      expect(modalityForModel(id)).toBe('image')
    }
  })

  it('does NOT misclassify plain gemini chat models as image', () => {
    for (const id of ['gemini-2.5-pro', 'gemini-2.5-flash', 'gemini-3-flash-agent', 'gemini-3.5-flash-extra-low']) {
      expect(modalityForModel(id)).toBe('chat')
      expect(isGeminiNativeImageModel(id)).toBe(false)
    }
    expect(isGeminiNativeImageModel('gemini-3.1-flash-image')).toBe(true)
    expect(isGeminiNativeImageModel('models/gemini-2.5-flash-image')).toBe(true)
  })

  it('classifies the served video families', () => {
    for (const id of ['veo-3.1-generate-preview', 'veo-2.0-generate-001', 'doubao-seedance-1-0-pro-250528']) {
      expect(modalityForModel(id)).toBe('video')
    }
  })

  it('defaults everything else (and empty) to chat', () => {
    for (const id of ['gpt-5.5', 'claude-sonnet-4-6', 'doubao-seed-1-6-250615', 'gemini-2.5-pro', '']) {
      expect(modalityForModel(id)).toBe('chat')
    }
  })

  it('is case/whitespace tolerant', () => {
    expect(modalityForModel('  GPT-Image-1 ')).toBe('image')
    expect(modalityForModel('VEO-3.0-GENERATE-001')).toBe('video')
  })
})

describe('extractImageItems', () => {
  it('normalizes url entries with revised_prompt', () => {
    const items = extractImageItems({
      data: [{ url: 'https://cdn.example/img.png', revised_prompt: 'a red fox' }]
    })
    expect(items).toEqual([{ src: 'https://cdn.example/img.png', revisedPrompt: 'a red fox' }])
  })

  it('wraps b64_json into a data URI', () => {
    const items = extractImageItems({ data: [{ b64_json: 'aGVsbG8=' }] })
    expect(items).toEqual([{ src: 'data:image/png;base64,aGVsbG8=', revisedPrompt: undefined }])
  })

  it('captures s3_key when the gateway offloaded the image (reload re-presign)', () => {
    const items = extractImageItems({
      data: [{ url: 'https://s3.example/media/images/abc.png', s3_key: 'media/images/abc.png' }]
    })
    expect(items).toEqual([
      { src: 'https://s3.example/media/images/abc.png', revisedPrompt: undefined, s3Key: 'media/images/abc.png' }
    ])
  })

  it('returns empty for malformed payloads', () => {
    expect(extractImageItems(null)).toEqual([])
    expect(extractImageItems({ data: 'oops' })).toEqual([])
    expect(extractImageItems({ data: [{}] })).toEqual([])
  })

  it('drops non-http(s) urls — src lands in <a :href>', () => {
    expect(extractImageItems({ data: [{ url: 'javascript:alert(1)' }] })).toEqual([])
    expect(extractImageItems({ data: [{ url: 'data:text/html;base64,PGI+' }] })).toEqual([])
    expect(extractImageItems({ data: [{ url: 'HTTPS://cdn.example/x.png' }] })).toHaveLength(1)
  })
})

describe('extractChatImageItems (gemini image via /v1/chat/completions)', () => {
  const chat = (content: unknown): unknown => ({ choices: [{ message: { content } }] })

  it('pulls the markdown data-image URI out of message content (verified prod shape)', () => {
    // Real prod response: content = "![image](data:image/jpeg;base64,…)".
    const items = extractChatImageItems(chat('![image](data:image/jpeg;base64,aGVsbG8=)'))
    expect(items).toEqual([{ src: 'data:image/jpeg;base64,aGVsbG8=' }])
  })

  it('handles multiple images and a leading caption', () => {
    const items = extractChatImageItems(
      chat('here:\n![a](data:image/png;base64,QQ==) and ![b](data:image/jpeg;base64,Qg==)')
    )
    expect(items.map((i) => i.src)).toEqual(['data:image/png;base64,QQ==', 'data:image/jpeg;base64,Qg=='])
  })

  it('handles structured content parts (image_url / inline_data)', () => {
    expect(
      extractChatImageItems(chat([{ type: 'image_url', image_url: { url: 'https://cdn.example/x.png' } }]))
    ).toEqual([{ src: 'https://cdn.example/x.png' }])
    expect(extractChatImageItems(chat([{ inline_data: { mime_type: 'image/webp', data: 'd2VicA==' } }]))).toEqual([
      { src: 'data:image/webp;base64,d2VicA==' }
    ])
  })

  it('returns empty for text-only / malformed responses', () => {
    expect(extractChatImageItems(chat('just text, no image'))).toEqual([])
    expect(extractChatImageItems(null)).toEqual([])
    expect(extractChatImageItems({ choices: 'oops' })).toEqual([])
    expect(extractChatImageItems({ choices: [{}] })).toEqual([])
  })

  it('drops non-image / oversized data URIs (same guard as extractImageItems)', () => {
    expect(extractChatImageItems(chat('![x](data:text/html;base64,PGI+)'))).toEqual([])
    const huge = 'A'.repeat(64 * 1024 * 1024 + 1)
    expect(extractChatImageItems(chat(`![x](data:image/png;base64,${huge})`))).toEqual([])
  })
})

describe('video task helpers', () => {
  it('reads the vt_ task id from submit response (id or task_id)', () => {
    expect(extractVideoTaskId({ id: 'vt_abc' })).toBe('vt_abc')
    expect(extractVideoTaskId({ task_id: 'vt_def' })).toBe('vt_def')
    expect(extractVideoTaskId({})).toBe('')
  })

  it('maps upstream status vocabulary to terminal states', () => {
    expect(videoStateFromFetch({ status: 'succeeded' })).toBe('succeeded')
    expect(videoStateFromFetch({ status: 'SUCCESS' })).toBe('succeeded')
    expect(videoStateFromFetch({ status: 'completed' })).toBe('succeeded')
    expect(videoStateFromFetch({ status: 'failed' })).toBe('failed')
    expect(videoStateFromFetch({ status: 'failure' })).toBe('failed')
    expect(videoStateFromFetch({ status: 'error' })).toBe('failed')
    expect(videoStateFromFetch({ status: 'processing' })).toBe('processing')
    expect(videoStateFromFetch({})).toBe('processing')
  })

  it('maps the Vertex/Gemini operation shape (done/error, no status string) — veo', () => {
    // veo-3.1 returns a long-running operation: done flips true, error absent on success.
    expect(videoStateFromFetch({ done: false })).toBe('processing')
    expect(videoStateFromFetch({ done: true, response: { videos: [{ bytesBase64Encoded: 'AAAA' }] } })).toBe('succeeded')
    expect(videoStateFromFetch({ done: true, error: { message: 'RESOURCE_EXHAUSTED' } })).toBe('failed')
    // An empty error object is not a failure (done governs).
    expect(videoStateFromFetch({ done: true, error: {} })).toBe('succeeded')
    expect(videoStateFromFetch({ done: false, error: { message: 'transient' } })).toBe('failed')
  })

  it('extracts inline base64 video bytes from the Vertex operation shape', () => {
    expect(
      extractVideoUrl({ response: { videos: [{ bytesBase64Encoded: 'QUJD', mimeType: 'video/mp4' }] } })
    ).toBe('data:video/mp4;base64,QUJD')
    // mimeType honored when it is a video/* type.
    expect(
      extractVideoUrl({ response: { videos: [{ bytesBase64Encoded: 'QUJD', mimeType: 'video/webm' }] } })
    ).toBe('data:video/webm;base64,QUJD')
    // response.bytesBase64Encoded / response.video fallbacks.
    expect(extractVideoUrl({ response: { bytesBase64Encoded: 'WFla' } })).toBe('data:video/mp4;base64,WFla')
    expect(extractVideoUrl({ response: { video: 'WFla' } })).toBe('data:video/mp4;base64,WFla')
  })

  it('rejects an oversized inline base64 payload (client tab-DoS guard)', () => {
    // > MAX_INLINE_B64_CHARS (64 MiB of base64 chars). Allocated once, reused.
    const huge = 'A'.repeat(64 * 1024 * 1024 + 1)
    expect(extractVideoUrl({ response: { videos: [{ bytesBase64Encoded: huge }] } })).toBe('')
    expect(extractVideoUrl({ response: { bytesBase64Encoded: huge } })).toBe('')
    expect(extractImageItems({ data: [{ b64_json: huge }] })).toEqual([])
    // a normal-size payload still passes
    expect(extractVideoUrl({ response: { videos: [{ bytesBase64Encoded: 'QUJD' }] } })).toBe('data:video/mp4;base64,QUJD')
  })

  it('refuses a non-video mimeType on the inline data URI — src/href XSS guard', () => {
    // A compromised relay must not be able to smuggle text/html (or any
    // non-video scheme) onto a URI that lands in <video :src> / <a :href>.
    expect(
      extractVideoUrl({ response: { videos: [{ bytesBase64Encoded: 'QUJD', mimeType: 'text/html' }] } })
    ).toBe('data:video/mp4;base64,QUJD')
    expect(
      extractVideoUrl({ response: { videos: [{ bytesBase64Encoded: 'QUJD', mimeType: 'image/svg+xml' }] } })
    ).toBe('data:video/mp4;base64,QUJD')
  })

  it('extracts the video url from known vendor shapes', () => {
    expect(extractVideoUrl({ content: { video_url: 'https://v/ark.mp4' } })).toBe('https://v/ark.mp4')
    expect(extractVideoUrl({ data: { video_url: 'https://v/a.mp4' } })).toBe('https://v/a.mp4')
    expect(extractVideoUrl({ video_url: 'https://v/b.mp4' })).toBe('https://v/b.mp4')
    expect(extractVideoUrl({ data: { url: 'https://v/c.mp4' } })).toBe('https://v/c.mp4')
  })

  it('deep-scans unknown shapes for a video-ish url and gives up cleanly', () => {
    expect(extractVideoUrl({ result: { outputs: [{ download_url: 'https://v/d.mp4?sig=1' }] } })).toBe(
      'https://v/d.mp4?sig=1'
    )
    expect(extractVideoUrl({ result: { thumb: 'https://v/preview.jpg' } })).toBe('')
    expect(extractVideoUrl(null)).toBe('')
  })

  it('reads a done:true top-level video_url response', () => {
    // Some upstreams return only a hosted video URL on terminal success. TokenKey
    // passes that URL through instead of rehosting it.
    const response = { done: true, response: {}, video_url: 'https://cdn.example.test/videos/vt_x.mp4' }
    expect(videoStateFromFetch(response)).toBe('succeeded')
    expect(extractVideoUrl(response)).toBe('https://cdn.example.test/videos/vt_x.mp4')
  })
})
