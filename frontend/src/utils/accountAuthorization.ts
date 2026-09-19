import type { Account } from '@/types'

/** Routing shared by account-row authorization links and the action menu. */
export function accountAuthorizationAction(account: Pick<Account, 'platform' | 'type' | 'parent_account_id' | 'extra'>): 'oauth' | 'import' | 'cursor' | null {
  if (account.parent_account_id != null) return null
  if (account.extra?.upstream_provider === 'cursor') return 'cursor'
  if (account.type !== 'oauth' && account.type !== 'setup-token') return null
  if (account.platform === 'kiro') return 'import'
  if (['anthropic', 'openai', 'gemini', 'antigravity', 'grok'].includes(account.platform)) return 'oauth'
  return null
}
