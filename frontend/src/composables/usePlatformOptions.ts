import { computed, toValue, type ComputedRef, type MaybeRefOrGetter } from 'vue'
import { GATEWAY_PLATFORMS } from '@/constants/gatewayPlatforms'
import type { AccountPlatform } from '@/types'

/**
 * Display label per platform. Brand names are not localized today
 * (matches the existing Anthropic / OpenAI / Gemini / Antigravity convention
 * already hardcoded in 5+ admin views before this composable existed).
 */
export const PLATFORM_LABELS: Record<AccountPlatform, string> = {
  anthropic: 'Anthropic',
  openai: 'OpenAI',
  gemini: 'Gemini',
  antigravity: 'Antigravity',
  newapi: 'New API',
}

export interface PlatformOption {
  value: AccountPlatform
  label: string
  [key: string]: unknown
}

export interface PlatformFilterOption {
  value: '' | AccountPlatform
  label: string
  [key: string]: unknown
}

/**
 * Single source of truth for admin-UI platform option lists.
 *
 * Drives every "select / filter by platform" dropdown so that adding the
 * Nth platform to {@link GATEWAY_PLATFORMS} auto-propagates to every picker
 * (Jobs minimalism + OPC automation: one canonical list, one regression test).
 *
 * @example
 *   const { options } = usePlatformOptions()
 *   // i18n-reactive caller: pass a getter so language switches re-evaluate.
 *   const filterOpts = optionsWithAll(() => t('admin.allPlatforms'))
 */
export function usePlatformOptions(): {
  options: ComputedRef<PlatformOption[]>
  optionsWithAll: (
    allLabel: MaybeRefOrGetter<string>,
  ) => ComputedRef<PlatformFilterOption[]>
} {
  const options = computed<PlatformOption[]>(() =>
    GATEWAY_PLATFORMS.map((p) => ({ value: p, label: PLATFORM_LABELS[p] })),
  )

  // `allLabel` accepts a string, ref, or getter so callers passing
  // `() => t('...')` keep i18n reactivity (the previous string-only signature
  // captured a snapshot at composition time and went stale on language switch).
  const optionsWithAll = (
    allLabel: MaybeRefOrGetter<string>,
  ): ComputedRef<PlatformFilterOption[]> =>
    computed<PlatformFilterOption[]>(() => [
      { value: '', label: toValue(allLabel) },
      ...options.value,
    ])

  return { options, optionsWithAll }
}
