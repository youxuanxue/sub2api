import type { ImageGenerationCapability } from '@/api/api-key-capabilities'
import { isGeminiNativeImageModel, extractImageItems, extractChatImageItems } from '@/constants/playgroundMedia.tk'
import { resolveAvailableModels, type ImageSizeOption } from '@/constants/studioMediaPresentations.tk'

export interface ImageGenerationPlan {
  endpoint: string
  sizes: ImageSizeOption[]
  counts: number[]
  inputImage: boolean
  softAspectRatio: boolean
  ratioField: boolean
}
export interface ImageGenerationOptions { ratio: string; n: number; inputImage: string }

/** One complete profile, never a union of incompatible account options. */
export function imageGenerationPlan(model: string, profiles?: ImageGenerationCapability[], native = false): ImageGenerationPlan {
  const preferredEndpoint = native ? `/v1beta/models/${encodeURIComponent(model)}:generateContent`
    : isGeminiNativeImageModel(model) ? '/v1/chat/completions' : '/v1/images/generations'
  const nativeEndpoint = `/v1beta/models/${encodeURIComponent(model)}:generateContent`
  const supported = (profiles ?? []).filter(p => ['/v1/chat/completions', '/v1/images/generations', nativeEndpoint].includes(p.endpoint))
  const preferred = supported.filter(p => p.endpoint === preferredEndpoint)
  const candidates = preferred.length ? preferred : supported
  const profile = [...candidates].sort((a, b) => b.aspect_ratios.length - a.aspect_ratios.length || Number(b.input_image) - Number(a.input_image) || b.counts.length - a.counts.length)[0]
  const endpoint = profile?.endpoint ?? preferredEndpoint
  const ratioField = endpoint !== '/v1/images/generations' || !!profile?.soft_aspect_ratio || /^gpt-image-/i.test(model)
  const presentation = resolveAvailableModels('image', new Set([model]), new Map())[0]?.presentation
  return {
    endpoint, ratioField,
    sizes: profile ? ratioField ? profile.aspect_ratios.map(ratio => ({ ratio, value: ratio })) : presentation?.imageSizes ?? [] : [],
    counts: profile?.counts.length ? profile.counts : [1],
    inputImage: !!profile?.input_image && endpoint === '/v1/chat/completions',
    softAspectRatio: !!profile?.soft_aspect_ratio || /^gpt-image-/i.test(model),
  }
}

/** Switch, restore, navigation and send all use this normalization. */
export function normalizeImageOptions(plan: ImageGenerationPlan, options: Partial<ImageGenerationOptions> = {}): ImageGenerationOptions {
  const pixels = /^(\d+)x(\d+)$/.exec(options.ratio ?? '')
  let legacyRatio = ''
  if (pixels) {
    const width = Number(pixels[1]), height = Number(pixels[2])
    let a = width, b = height
    while (b) { const remainder = a % b; a = b; b = remainder }
    if (a > 0 && Number.isSafeInteger(width) && Number.isSafeInteger(height)) legacyRatio = `${width / a}:${height / a}`
  }
  const size = plan.sizes.find(s => s.ratio === options.ratio || s.value === options.ratio || s.ratio === legacyRatio) ?? plan.sizes[0]
  return { ratio: size?.ratio ?? '', n: plan.counts.includes(options.n ?? 1) ? options.n ?? 1 : plan.counts[0] ?? 1, inputImage: plan.inputImage ? options.inputImage ?? '' : '' }
}

export function buildImageRequest(model: string, prompt: string, plan: ImageGenerationPlan, options: Partial<ImageGenerationOptions> = {}) {
  const normalized = normalizeImageOptions(plan, options)
  const value = plan.sizes.find(s => s.ratio === normalized.ratio)?.value
  let body: Record<string, unknown>
  if (plan.endpoint.endsWith(':generateContent')) {
    const config: Record<string, unknown> = { responseModalities: ['TEXT', 'IMAGE'] }
    if (value) config.imageConfig = { aspectRatio: value }
    body = { contents: [{ role: 'user', parts: [{ text: prompt }] }], generationConfig: config }
  } else if (plan.endpoint === '/v1/chat/completions') {
    const content = normalized.inputImage ? [{ type: 'text', text: prompt }, { type: 'image_url', image_url: { url: normalized.inputImage } }] : prompt
    body = { model, messages: [{ role: 'user', content }], stream: false }
    if (value) body.extra_body = { google: { image_config: { aspect_ratio: value } } }
  } else {
    body = { model, prompt }
    if (value) body[plan.ratioField ? 'aspect_ratio' : 'size'] = value
    if (normalized.n > 1) body.n = normalized.n
  }
  return { endpoint: plan.endpoint, body }
}

export function imageStudioPath(model: string, options: Partial<ImageGenerationOptions>, keyId?: number | null): string {
  const query = new URLSearchParams({ mode: 'image', model })
  if (options.ratio) query.set('ratio', options.ratio)
  if (options.n && options.n > 1) query.set('n', String(options.n))
  if (keyId) query.set('key', String(keyId))
  return `/studio?${query}`
}

/** Reuse the existing image-part parser for native Gemini responses too. */
export function extractGeneratedImages(raw: unknown, plan: ImageGenerationPlan) {
  if (plan.endpoint === '/v1/images/generations') return extractImageItems(raw)
  if (plan.endpoint.endsWith(':generateContent')) {
    const candidates = (raw as { candidates?: { content?: { parts?: unknown[] } }[] } | null)?.candidates
    return extractChatImageItems({ choices: Array.isArray(candidates) ? candidates.map(c => ({ message: { content: c.content?.parts } })) : [] })
  }
  return extractChatImageItems(raw)
}
