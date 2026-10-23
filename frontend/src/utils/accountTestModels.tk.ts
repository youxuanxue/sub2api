import { isGeminiNativeImageModel, modalityForModel } from '@/constants/playgroundMedia.tk'
import { PLATFORM_ANTIGRAVITY, PLATFORM_GEMINI } from '@/constants/gatewayPlatforms'

// Shared by both account test dialogs. Keep text connectivity probes ahead of
// media even when a newly served model has no curated display preference.
// 这份列表被 Antigravity 与 Gemini 两个平台的账号测试对话框共用，所以只放
// 两边都在供应的模型。gemini-3.7-flash 已于 2026-10-23 从 AG 面退役（上游
// 换代到 3.8，三个档位 wire 一致 404），放在这里会让运维测 AG 账号时打一个
// 必然失败的模型、误判成账号坏了。它在 newapi/Vertex 侧仍可用，但那是按
// 实际 served 模型兜底选取，不需要在这份偏好表里显式提前。
const geminiTestPreference = [
  'gemini-3.8-flash',
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
