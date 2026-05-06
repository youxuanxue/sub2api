import { describe, expect, it } from 'vitest'

import {
  ACCOUNT_COLUMNS_TS_DEFAULT_VISIBLE_KEY,
  migrateAccountTimestampColumnsVisibleOnce,
} from '../migrateAccountColumnsTs'

const makeStorage = (initial: Record<string, string> = {}) => {
  const data = { ...initial }
  return {
    data,
    getItem: (k: string) => (k in data ? data[k] : null),
    setItem: (k: string, v: string) => {
      data[k] = v
    },
  }
}

describe('migrateAccountTimestampColumnsVisibleOnce', () => {
  it('removes last_used_at and expires_at from the hidden set on first run', () => {
    const hidden = new Set(['last_used_at', 'expires_at', 'notes'])
    const storage = makeStorage()

    const ran = migrateAccountTimestampColumnsVisibleOnce(hidden, storage)

    expect(ran).toBe(true)
    expect(hidden.has('last_used_at')).toBe(false)
    expect(hidden.has('expires_at')).toBe(false)
    expect(hidden.has('notes')).toBe(true)
    expect(storage.data[ACCOUNT_COLUMNS_TS_DEFAULT_VISIBLE_KEY]).toBe('1')
  })

  it('is a no-op on second run (sentinel already set)', () => {
    const hidden = new Set(['last_used_at'])
    const storage = makeStorage({ [ACCOUNT_COLUMNS_TS_DEFAULT_VISIBLE_KEY]: '1' })

    const ran = migrateAccountTimestampColumnsVisibleOnce(hidden, storage)

    expect(ran).toBe(false)
    expect(hidden.has('last_used_at')).toBe(true)
  })

  it('does not throw when neither column was previously hidden', () => {
    const hidden = new Set<string>(['notes'])
    const storage = makeStorage()

    const ran = migrateAccountTimestampColumnsVisibleOnce(hidden, storage)

    expect(ran).toBe(true)
    expect(hidden.has('notes')).toBe(true)
    expect(storage.data[ACCOUNT_COLUMNS_TS_DEFAULT_VISIBLE_KEY]).toBe('1')
  })

  it('returns false and does not mutate when storage throws on getItem', () => {
    const hidden = new Set(['last_used_at', 'expires_at'])
    const storage = {
      getItem: () => {
        throw new Error('quota exceeded')
      },
      setItem: () => {
        throw new Error('should not reach')
      },
    }

    const ran = migrateAccountTimestampColumnsVisibleOnce(hidden, storage)

    expect(ran).toBe(false)
    expect(hidden.has('last_used_at')).toBe(true)
    expect(hidden.has('expires_at')).toBe(true)
  })
})
