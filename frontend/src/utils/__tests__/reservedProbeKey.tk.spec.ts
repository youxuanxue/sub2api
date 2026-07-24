import { describe, expect, it } from 'vitest'

import {
  filterUserSelectableApiKeys,
  isReservedProbeApiKeyName,
  isUserSelectableApiKey,
} from '../reservedProbeKey.tk'

describe('reservedProbeKey.tk', () => {
  it('detects reserved probe key names', () => {
    expect(isReservedProbeApiKeyName('__tk_probe_openai_key')).toBe(true)
    expect(isReservedProbeApiKeyName('TK_FULLTEST_KEY')).toBe(false)
    expect(isReservedProbeApiKeyName('')).toBe(false)
  })

  it('filters inactive and probe keys from user pickers', () => {
    const keys = [
      { name: '__tk_probe_newapi_srcgrp_16_key', status: 'active' as const },
      { name: 'trial', status: 'active' as const },
      { name: 'China', status: 'inactive' as const },
    ]
    expect(isUserSelectableApiKey(keys[0])).toBe(false)
    expect(isUserSelectableApiKey(keys[1])).toBe(true)
    expect(isUserSelectableApiKey(keys[2])).toBe(false)
    expect(filterUserSelectableApiKeys(keys)).toEqual([keys[1]])
  })
})
