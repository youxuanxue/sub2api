import type { Account } from '@/types'

// Only Google verification destinations are actionable; never turn arbitrary
// upstream error text into an operator navigation target.
export function googleVerificationURL(value?: string | null): string {
  if (!value) return ''
  try {
    const url = new URL(value)
    return url.protocol === 'https:' && !url.username && !url.password && !url.port &&
      ['accounts.google.com', 'support.google.com'].includes(url.hostname)
      ? url.href : ''
  } catch {
    return ''
  }
}

export function accountGoogleVerificationURL(account: Pick<Account, 'platform' | 'temp_unschedulable_reason' | 'error_message'>): string {
  if (account.platform !== 'antigravity') return ''
  // The gateway appends this marker after the (possibly truncated) error text.
  // Include error_message for legacy errors and failed cooldown persistence.
  for (const reason of [account.temp_unschedulable_reason, account.error_message]) {
    const value = reason?.match(/\bvalidation_url:\s*(https:\/\/[^\s|<>"']+)/i)?.[1]
    const url = googleVerificationURL(value)
    if (url) return url
  }
  return ''
}
