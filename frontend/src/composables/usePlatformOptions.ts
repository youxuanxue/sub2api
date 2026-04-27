import { computed, toValue, type ComputedRef, type MaybeRefOrGetter } from 'vue'
import { GATEWAY_PLATFORMS } from '@/constants/gatewayPlatforms'
import type { AccountPlatform } from '@/types'

export const PLATFORM_LABELS: Record<AccountPlatform, string> = {
  anthropic: 'Anthropic',
  openai: 'OpenAI',
  gemini: 'Gemini',
  antigravity: 'Antigravity',
  newapi: 'Extension Engine',
}

export function getPlatformLabel(platform: string | null | undefined): string {
  if (!platform) return '-'
  return PLATFORM_LABELS[platform as AccountPlatform] ?? platform
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

export function usePlatformOptions(): {
  options: ComputedRef<PlatformOption[]>
  optionsWithAll: (
    allLabel: MaybeRefOrGetter<string>,
  ) => ComputedRef<PlatformFilterOption[]>
} {
  const options = computed<PlatformOption[]>(() =>
    GATEWAY_PLATFORMS.map((p) => ({ value: p, label: PLATFORM_LABELS[p] })),
  )

  const optionsWithAll = (
    allLabel: MaybeRefOrGetter<string>,
  ): ComputedRef<PlatformFilterOption[]> =>
    computed<PlatformFilterOption[]>(() => [
      { value: '', label: toValue(allLabel) },
      ...options.value,
    ])

  return { options, optionsWithAll }
}
