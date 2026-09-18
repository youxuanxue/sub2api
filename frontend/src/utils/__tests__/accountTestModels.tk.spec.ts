import { describe, expect, it } from 'vitest'
import { supportsGeminiImageTest, sortAccountTestModels } from '../accountTestModels.tk'
import { PLATFORM_ANTIGRAVITY, PLATFORM_GEMINI, PLATFORM_OPENAI } from '@/constants/gatewayPlatforms'

describe('shared account test model policy', () => {
  it.each(['nano-2', 'nano-pro', 'gemini-3.1-flash-image'])('enables image prompts for %s on Google accounts only', (id) => {
    for (const platform of [PLATFORM_ANTIGRAVITY, PLATFORM_GEMINI]) expect(supportsGeminiImageTest(platform, id)).toBe(true)
    expect(supportsGeminiImageTest(PLATFORM_OPENAI, id)).toBe(false)
    expect(supportsGeminiImageTest(PLATFORM_GEMINI, 'gemini-3.8-flash')).toBe(false)
  })
  it('prefers text connectivity over media, including uncurated future ids', () => {
    const models = [{ id: 'nano-pro' }, { id: 'gemini-future-text' }, { id: 'gemini-3.8-flash' }]
    expect(sortAccountTestModels(models).map((m) => m.id)).toEqual(['gemini-3.8-flash', 'gemini-future-text', 'nano-pro'])
    expect(models[0].id).toBe('nano-pro')
  })
})
