// TokenKey-only. An anthropic + apikey "mirror stub" relays prod traffic to an
// edge node (credentials.base_url points at an internal api-<edge>.tokenkey.dev
// host). credentials.mirror_platform declares WHICH edge pool's live Σ schedulable
// concurrency the surface-C reconciler mirrors onto this stub's concurrency column.
//
// The stub's own platform is always `anthropic` (its transport is anthropic-apikey
// relay), so it cannot self-describe whether it represents the edge's anthropic pool
// or its kiro pool — this field is that declaration. Default = anthropic, matching
// the backend default in
// backend/internal/service/anthropic_config_reconciler.go::mirrorCapacityPlatform.

export type MirrorPlatform = 'anthropic' | 'kiro'

export interface MirrorPlatformOption {
  value: MirrorPlatform
  // Brand names — identical across locales, so no i18n key (the field label/hint
  // around the select ARE translated).
  label: string
}

export const MIRROR_PLATFORM_OPTIONS: MirrorPlatformOption[] = [
  { value: 'anthropic', label: 'Anthropic' },
  { value: 'kiro', label: 'Kiro' },
]

// normalizeMirrorPlatform mirrors the backend default: empty / unknown → 'anthropic'.
export function normalizeMirrorPlatform(raw: unknown): MirrorPlatform {
  return raw === 'kiro' ? 'kiro' : 'anthropic'
}
