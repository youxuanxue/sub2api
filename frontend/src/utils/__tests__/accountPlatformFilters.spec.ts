import { describe, expect, it } from 'vitest'

import { accountMatchesPlatformFilter, isKiroRelayStubAccount } from '../accountPlatformFilters'
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

  it('excludes Kiro relay stubs from the Anthropic platform filter', () => {
    const nativeKiro = account({ platform: 'kiro', type: 'oauth' })
    const kiroStub = account({
      platform: 'anthropic',
      type: 'apikey',
      credentials: {
        base_url: 'https://api-us5.tokenkey.dev',
        mirror_platform: 'kiro'
      }
    })
    const plainAnthropic = account({
      platform: 'anthropic',
      type: 'oauth'
    })

    expect(isKiroRelayStubAccount(kiroStub)).toBe(true)
    expect(accountMatchesPlatformFilter(kiroStub, 'anthropic')).toBe(false)
    expect(accountMatchesPlatformFilter(plainAnthropic, 'anthropic')).toBe(true)
    expect(accountMatchesPlatformFilter(nativeKiro, 'anthropic')).toBe(false)
  })
})
