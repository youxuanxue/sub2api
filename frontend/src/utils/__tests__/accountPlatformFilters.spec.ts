import { describe, expect, it } from 'vitest'

import { accountMatchesPlatformFilter } from '../accountPlatformFilters'
import type { Account } from '@/types'

const account = (overrides: Partial<Account>): Account => ({
  id: 1,
  name: 'account',
  platform: 'anthropic',
  type: 'oauth',
  status: 'active',
  schedulable: true,
  credentials: {},
  created_at: '2026-06-24T00:00:00Z',
  updated_at: '2026-06-24T00:00:00Z',
  ...overrides,
} as Account)

describe('account platform filters', () => {
  it('matches only native Kiro accounts', () => {
    const nativeKiro = account({ platform: 'kiro', type: 'oauth' })

    expect(accountMatchesPlatformFilter(nativeKiro, 'kiro')).toBe(true)
    expect(accountMatchesPlatformFilter(account({ platform: 'anthropic' }), 'kiro')).toBe(false)
    expect(accountMatchesPlatformFilter(account({ platform: 'anthropic' }), 'anthropic')).toBe(true)
  })
})
