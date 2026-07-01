import type { Account } from '@/types'

const EMAIL_PATTERN = /^[^\s@]+@[^\s@]+\.[^\s@]+$/

export function resolveAccountContactEmail(account: Pick<Account, 'extra' | 'credentials'> | null | undefined): string {
  if (!account) return ''
  const extra = (account.extra || {}) as Record<string, unknown>
  const credentials = (account.credentials || {}) as Record<string, unknown>
  for (const src of [extra, credentials]) {
    for (const key of ['email_address', 'email'] as const) {
      const value = src[key]
      if (typeof value === 'string' && value.trim()) {
        return value.trim()
      }
    }
  }
  return ''
}

export function isValidAccountContactEmail(email: string): boolean {
  const trimmed = email.trim()
  if (!trimmed) return true
  return EMAIL_PATTERN.test(trimmed)
}

export function withAccountContactEmail<T extends { contact_email?: string }>(
  payload: T,
  email: string
): T {
  return { ...payload, contact_email: email.trim() }
}
