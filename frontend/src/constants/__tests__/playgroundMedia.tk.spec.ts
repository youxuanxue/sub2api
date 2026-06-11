import { describe, expect, it } from 'vitest'
import {
  extractImageItems,
  extractVideoTaskId,
  extractVideoUrl,
  modalityForModel,
  videoStateFromFetch
} from '@/constants/playgroundMedia.tk'

describe('modalityForModel', () => {
  it('classifies the served image families', () => {
    for (const id of ['gpt-image-1', 'gpt-image-2-2026-04-21', 'imagen-4.0-generate-001', 'doubao-seedream-4-0-250828']) {
      expect(modalityForModel(id)).toBe('image')
    }
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

  it('returns empty for malformed payloads', () => {
    expect(extractImageItems(null)).toEqual([])
    expect(extractImageItems({ data: 'oops' })).toEqual([])
    expect(extractImageItems({ data: [{}] })).toEqual([])
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
    expect(videoStateFromFetch({ status: 'failed' })).toBe('failed')
    expect(videoStateFromFetch({ status: 'failure' })).toBe('failed')
    expect(videoStateFromFetch({ status: 'processing' })).toBe('processing')
    expect(videoStateFromFetch({})).toBe('processing')
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
})
