import { describe, expect, it } from 'vitest'
import {
  isValidAccountEmail,
  resolveAccountEmail,
  withAccountEmail
} from '@/utils/accountEmail.tk'

describe('accountEmail.tk', () => {
  it('resolveAccountEmail prefers extra.email_address', () => {
    expect(
      resolveAccountEmail({
        extra: { email_address: ' extra@example.com ' },
        credentials: { email: 'cred@example.com' }
      })
    ).toBe('extra@example.com')
  })

  it('withAccountEmail trims account_email', () => {
    expect(withAccountEmail({ name: 'a' }, '  user@example.com  ')).toEqual({
      name: 'a',
      account_email: 'user@example.com'
    })
  })

  it('isValidAccountEmail accepts empty and rejects invalid', () => {
    expect(isValidAccountEmail('')).toBe(true)
    expect(isValidAccountEmail('user@example.com')).toBe(true)
    expect(isValidAccountEmail('not-an-email')).toBe(false)
  })
})
