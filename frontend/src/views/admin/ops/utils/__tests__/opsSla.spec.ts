import { describe, expect, it } from 'vitest'

import { resolveOpsSlaPercent, shouldFillHeaderOverview } from '../opsSla'

describe('resolveOpsSlaPercent', () => {
  it('returns sla percent when the window has requests', () => {
    expect(resolveOpsSlaPercent({ sla: 0.9966, request_count_total: 1178 })).toBeCloseTo(99.66)
  })

  it('hides sla when request_count_total is missing or zero', () => {
    expect(resolveOpsSlaPercent({ sla: 0.9966 })).toBeNull()
    expect(resolveOpsSlaPercent({ sla: 1, request_count_total: 0 })).toBeNull()
  })

  it('does not use a backend field that is never sent', () => {
    const overview = { sla: 0.9818, request_count_total: 110, request_count_sla: undefined }
    expect(resolveOpsSlaPercent(overview)).toBeCloseTo(98.18)
  })
})

describe('shouldFillHeaderOverview', () => {
  it('fills the header from overview when snapshot is still pending', () => {
    expect(shouldFillHeaderOverview(false, false)).toBe(true)
  })

  it('does not start a second overview after snapshot or header already applied', () => {
    expect(shouldFillHeaderOverview(true, false)).toBe(false)
    expect(shouldFillHeaderOverview(false, true)).toBe(false)
  })
})
