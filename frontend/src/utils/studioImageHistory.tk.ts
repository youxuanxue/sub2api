import type { ImageHistoryItem } from '@/composables/useMediaLibrary'

/** Mint a stable, unique id for a Studio image history row (Vue :key + IndexedDB). */
export function studioImageHistoryId(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID()
  }
  return `${Date.now()}-${Math.random().toString(36).slice(2, 11)}`
}

/** True when src bytes are mirrored in IndexedDB or re-minted on mount — not safe to persist. */
export function isEphemeralImageSrc(src: string | undefined): boolean {
  const s = (src ?? '').trim()
  if (!s) return true
  return /^data:/i.test(s) || s.startsWith('blob:') || /^https?:\/\//i.test(s)
}

/** Tooltip: user prompt, optional upstream revised_prompt, and served model id. */
export function imageHistoryPromptTitle(
  img: Pick<ImageHistoryItem, 'prompt' | 'revisedPrompt' | 'model'>,
  revisedPromptHint: (text: string) => string
): string {
  const lines = [img.prompt]
  if (img.revisedPrompt && img.revisedPrompt.trim() !== img.prompt.trim()) {
    lines.push(revisedPromptHint(img.revisedPrompt))
  }
  lines.push(img.model)
  return lines.join('\n')
}

/** Resolve a priced model row from a history item's served id (ImageStudio reuse). */
export function matchImageHistoryModel<T extends { servedId: string; model: { modelId: string } }>(
  models: readonly T[],
  servedOrModelId: string
): T | undefined {
  return models.find((r) => r.servedId === servedOrModelId || r.model.modelId === servedOrModelId)
}
