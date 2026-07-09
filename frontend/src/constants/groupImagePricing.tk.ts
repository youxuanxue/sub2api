// TokenKey-only: admin group form surfaces image-generation billing controls for
// platforms that can actually serve /v1/images/generations or gemini-native chat
// image. newapi was omitted from the original UI gate even though migration 134
// only backfilled openai/gemini/antigravity — Vertex (41) / Seedream (45) media
// groups need the same allow_image_generation toggle.

export const GROUP_IMAGE_PRICING_PLATFORMS = [
  'openai',
  'gemini',
  'antigravity',
  'newapi',
  'grok',
] as const

export type GroupImagePricingPlatform = (typeof GROUP_IMAGE_PRICING_PLATFORMS)[number]

export function supportsGroupImagePricing(platform: string | undefined | null): boolean {
  const p = (platform || '').trim().toLowerCase()
  return (GROUP_IMAGE_PRICING_PLATFORMS as readonly string[]).includes(p)
}
