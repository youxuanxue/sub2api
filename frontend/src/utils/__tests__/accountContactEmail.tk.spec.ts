import { describe, expect, it } from 'vitest'
import {
  isValidAccountContactEmail,
  resolveAccountContactEmail,
  withAccountContactEmail
} from '@/utils/accountContactEmail.tk'

describe('accountContactEmail.tk', () => {
  it('resolveAccountContactEmail prefers extra.email_address', () => {
    expect(
      resolveAccountContactEmail({
        extra: { email_address: ' extra@example.com ' },
        credentials: { email: 'cred@example.com' }
      })
    ).toBe('extra@example.com')
  })

  it('withAccountContactEmail trims contact_email', () => {
    expect(withAccountContactEmail({ name: 'a' }, '  user@example.com  ')).toEqual({
      name: 'a',
      contact_email: 'user@example.com'
    })
  })

  it('isValidAccountContactEmail accepts empty and rejects invalid', () => {
    expect(isValidAccountContactEmail('')).toBe(true)
    expect(isValidAccountContactEmail('user@example.com')).toBe(true)
    expect(isValidAccountContactEmail('not-an-email')).toBe(false)
  })
})
