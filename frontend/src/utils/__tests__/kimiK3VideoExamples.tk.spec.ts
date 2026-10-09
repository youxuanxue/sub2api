import { describe, expect, it } from 'vitest'
import {
  KIMI_K3_REACHABLE_VIDEO_URL,
  isKimiK3VideoGuideModel,
  kimiK3VideoExamples,
} from '../kimiK3VideoExamples.tk'

describe('kimiK3VideoExamples', () => {
  it('only attaches the video guide to kimi-k3', () => {
    expect(isKimiK3VideoGuideModel('kimi-k3')).toBe(true)
    expect(isKimiK3VideoGuideModel('kimi-k2.6')).toBe(false)
  })

  it('chat examples use video_url object parts and prefer base64', () => {
    const files = kimiK3VideoExamples('curl', 'https://api.example', 'sk-test', 'hint')
    const chatB64 = files.find((f) => f.path.includes('chat + base64'))
    const chatUrl = files.find((f) => f.path.includes('chat + reachable HTTPS'))
    expect(chatB64?.hint).toBe('hint')
    expect(chatB64?.content).toContain('/v1/chat/completions')
    expect(chatB64?.content).toContain('video_url')
    expect(chatB64?.content).toContain('data:video/mp4;base64')
    expect(chatUrl?.content).toContain(KIMI_K3_REACHABLE_VIDEO_URL)
    expect(chatUrl?.content).not.toContain('/v1/responses')
  })

  it('responses example uses input_video rather than pretending Moonshot requires it', () => {
    const files = kimiK3VideoExamples('python', 'https://api.example/', 'sk-test', 'hint')
    const chat = files.find((f) => f.path.includes('video_chat'))
    const responses = files.find((f) => f.path.includes('video_responses'))
    expect(chat?.content).toContain('chat.completions.create')
    expect(chat?.content).toContain('video_url')
    expect(chat?.content).toContain('data:video/mp4;base64')
    expect(responses?.content).toContain('responses.create')
    expect(responses?.content).toContain('input_video')
    expect(responses?.content).toContain(KIMI_K3_REACHABLE_VIDEO_URL)
  })
})
