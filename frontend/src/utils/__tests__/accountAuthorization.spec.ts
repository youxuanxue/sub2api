import { describe, expect, it } from 'vitest'
import { accountAuthorizationAction } from '../accountAuthorization'
import type { Account } from '@/types'

describe('account authorization routing', () => {
  it.each(['anthropic', 'openai', 'gemini', 'antigravity', 'grok'] as const)('opens the OAuth flow for %s', platform => {
    expect(accountAuthorizationAction({ platform, type: 'oauth' })).toBe('oauth')
    expect(accountAuthorizationAction({ platform, type: 'apikey' })).toBeNull()
    expect(accountAuthorizationAction({ platform, type: 'oauth', parent_account_id: 1 })).toBeNull()
  })
  it('uses Kiro import, Cursor connect, and Claude setup-token authorization', () => {
    expect(accountAuthorizationAction({ platform: 'kiro', type: 'oauth' })).toBe('import')
    expect(accountAuthorizationAction({ platform: 'newapi', type: 'apikey', extra: { upstream_provider: 'cursor' } })).toBe('cursor')
    expect(accountAuthorizationAction({ platform: 'anthropic', type: 'setup-token' })).toBe('oauth')
    expect(accountAuthorizationAction({ platform: 'unknown', type: 'oauth' } as Account)).toBeNull()
  })
})
