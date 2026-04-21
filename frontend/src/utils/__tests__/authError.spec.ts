import { describe, expect, it } from 'vitest'
import { buildAuthErrorMessage } from '@/utils/authError'

describe('buildAuthErrorMessage', () => {
  it('prefers response detail message when available', () => {
    const message = buildAuthErrorMessage(
      {
        response: {
          data: {
            detail: 'detailed message',
            message: 'plain message'
          }
        },
      },
      { fallback: 'fallback' }
    )
    expect(message).toBe('detailed message')
  })

  it('falls back to response message when detail is unavailable', () => {
    const message = buildAuthErrorMessage(
      {
        response: {
          data: {
            message: 'plain message'
          }
        },
      },
      { fallback: 'fallback' }
    )
    expect(message).toBe('plain message')
  })

  it('falls back to error.message when response payload is unavailable', () => {
    const message = buildAuthErrorMessage(
      {
        message: 'error message'
      },
      { fallback: 'fallback' }
    )
    expect(message).toBe('error message')
  })

  it('uses fallback when no message can be extracted', () => {
    expect(buildAuthErrorMessage({}, { fallback: 'fallback' })).toBe('fallback')
  })

  it('reasonOverrides wins over response.data.detail when reason matches', () => {
    const message = buildAuthErrorMessage(
      {
        reason: 'TURNSTILE_VERIFICATION_FAILED',
        response: { data: { detail: 'turnstile verification failed', reason: 'TURNSTILE_VERIFICATION_FAILED' } }
      },
      {
        fallback: 'fallback',
        reasonOverrides: {
          TURNSTILE_VERIFICATION_FAILED: 'Stale verification token — refresh and try again'
        }
      }
    )
    expect(message).toBe('Stale verification token — refresh and try again')
  })

  it('reasonOverrides only applies when reason is in the override map', () => {
    const message = buildAuthErrorMessage(
      {
        reason: 'INVALID_CREDENTIALS',
        response: { data: { detail: 'wrong password', reason: 'INVALID_CREDENTIALS' } }
      },
      {
        fallback: 'fallback',
        reasonOverrides: { TURNSTILE_VERIFICATION_FAILED: 'refresh' }
      }
    )
    expect(message).toBe('wrong password')
  })

  it('reasonOverrides reads reason from response.data.reason when top-level missing', () => {
    const message = buildAuthErrorMessage(
      {
        response: { data: { detail: 'detailed', reason: 'TURNSTILE_VERIFICATION_FAILED' } }
      },
      {
        fallback: 'fallback',
        reasonOverrides: { TURNSTILE_VERIFICATION_FAILED: 'refresh hint' }
      }
    )
    expect(message).toBe('refresh hint')
  })
})
