import { isGeminiNativeImageModel, modalityForModel } from '@/constants/playgroundMedia.tk'
import { PLATFORM_ANTIGRAVITY, PLATFORM_GEMINI } from '@/constants/gatewayPlatforms'

// Shared by both account test dialogs. Keep text connectivity probes ahead of
// media even when a newly served model has no curated display preference.
const geminiTestPreference = [
  'gemini-3.8-flash', 'gemini-3.7-flash', 'gemini-3.6-flash',
  'gemini-3-flash', 'gemini-3-flash-preview', 'gemini-3.5-flash-lite',
]
const priority = new Map(geminiTestPreference.map((id, index) => [id, index]))

export function supportsGeminiImageTest(platform: string | undefined, model: string): boolean {
  return (platform === PLATFORM_GEMINI || platform === PLATFORM_ANTIGRAVITY) && isGeminiNativeImageModel(model)
}

export function sortAccountTestModels<T extends { id: string }>(models: readonly T[]): T[] {
  return [...models].sort((a, b) => {
    const media = Number(modalityForModel(a.id) !== 'chat') - Number(modalityForModel(b.id) !== 'chat')
    return media || (priority.get(a.id) ?? Number.MAX_SAFE_INTEGER) - (priority.get(b.id) ?? Number.MAX_SAFE_INTEGER)
  })
}
