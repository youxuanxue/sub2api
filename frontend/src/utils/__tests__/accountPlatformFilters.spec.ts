import { describe, expect, it } from 'vitest'

import {
  ACCOUNT_KIRO_STUB_PLATFORM_FILTER,
  accountMatchesPlatformFilter,
  isKiroRelayStubAccount
} from '../accountPlatformFilters'
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
  it('matches platform=kiro against both native Kiro accounts and Kiro relay stubs', () => {
    const nativeKiro = account({ platform: 'kiro', type: 'oauth' })
    const kiroStub = account({
      platform: 'anthropic',
      type: 'apikey',
      credentials: {
        base_url: 'https://api-us4.tokenkey.dev',
        mirror_platform: ' Kiro '
      }
    })
    const plainAnthropicEdge = account({
      platform: 'anthropic',
      type: 'apikey',
      credentials: {
        base_url: 'https://api-us4.tokenkey.dev',
        mirror_platform: 'anthropic'
      }
    })
    const kiroNonEdgeMirror = account({
      platform: 'anthropic',
      type: 'apikey',
      credentials: {
        base_url: 'https://api.anthropic.com',
        mirror_platform: 'kiro'
      }
    })

    expect(accountMatchesPlatformFilter(nativeKiro, 'kiro')).toBe(true)
    expect(accountMatchesPlatformFilter(kiroStub, 'kiro')).toBe(true)
    expect(accountMatchesPlatformFilter(plainAnthropicEdge, 'kiro')).toBe(false)
    expect(accountMatchesPlatformFilter(kiroNonEdgeMirror, 'kiro')).toBe(false)
  })

  it('keeps the virtual Kiro stub filter narrowed to relay stubs only', () => {
    const nativeKiro = account({ platform: 'kiro', type: 'oauth' })
    const kiroStub = account({
      platform: 'anthropic',
      type: 'apikey',
      credentials: {
        base_url: 'https://api-us5.tokenkey.dev',
        mirror_platform: 'kiro'
      }
    })

    expect(isKiroRelayStubAccount(kiroStub)).toBe(true)
    expect(accountMatchesPlatformFilter(kiroStub, ACCOUNT_KIRO_STUB_PLATFORM_FILTER)).toBe(true)
    expect(accountMatchesPlatformFilter(nativeKiro, ACCOUNT_KIRO_STUB_PLATFORM_FILTER)).toBe(false)
  })
})
