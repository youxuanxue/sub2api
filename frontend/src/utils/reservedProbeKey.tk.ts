import type { ApiKey } from '@/types'

/** Reserved ops/debug namespace — must not appear in user-facing key pickers. */
export const RESERVED_PROBE_KEY_PREFIX = '__tk_probe_'

export function isReservedProbeApiKeyName(name: string | null | undefined): boolean {
  return !!name && name.startsWith(RESERVED_PROBE_KEY_PREFIX)
}

/** Keys eligible for Quickstart / Studio / pricing pickers (not ops probe fixtures). */
export function isUserSelectableApiKey(key: Pick<ApiKey, 'name' | 'status'>): boolean {
  return key.status === 'active' && !isReservedProbeApiKeyName(key.name)
}

export function filterUserSelectableApiKeys<T extends Pick<ApiKey, 'name' | 'status'>>(keys: T[]): T[] {
  return keys.filter(isUserSelectableApiKey)
}
