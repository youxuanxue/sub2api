import { describe, expect, it } from 'vitest'
import { isUniversalKey } from '../studioUniversalKey.tk'
import type { ApiKey } from '@/types'

describe('studioUniversalKey', () => {
  it('detects universal keys by routing_mode or missing group', () => {
    expect(isUniversalKey({ id: 1, routing_mode: 'universal' } as ApiKey)).toBe(true)
    expect(isUniversalKey({ id: 2, group_id: null, group: null } as ApiKey)).toBe(true)
    expect(isUniversalKey({ id: 3, group: { id: 1, name: 'g' } } as ApiKey)).toBe(false)
  })
})
