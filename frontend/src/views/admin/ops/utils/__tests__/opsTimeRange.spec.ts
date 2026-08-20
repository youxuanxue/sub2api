import { describe, expect, it } from 'vitest'

import {
  OPS_MAX_WINDOW_MS,
  datetimeLocalToISO,
  resolveOpsCustomTimeRange,
  toDatetimeLocalValue
} from '../opsTimeRange'

describe('resolveOpsCustomTimeRange', () => {
  it('keeps an exact 30-day window unchanged', () => {
    const end = Date.parse('2026-08-20T05:37:00.000Z')
    const start = end - OPS_MAX_WINDOW_MS
    const startISO = new Date(start).toISOString()
    const endISO = new Date(end).toISOString()
    expect(resolveOpsCustomTimeRange(startISO, endISO)).toEqual({
      ok: true,
      startISO,
      endISO,
      clamped: false
    })
  })

  it('clamps a window one millisecond over 30 days to the max window ending at end', () => {
    const end = Date.parse('2026-08-20T05:37:00.000Z')
    const start = end - OPS_MAX_WINDOW_MS - 1
    const resolved = resolveOpsCustomTimeRange(new Date(start).toISOString(), new Date(end).toISOString())
    expect(resolved).toEqual({
      ok: true,
      startISO: new Date(end - OPS_MAX_WINDOW_MS).toISOString(),
      endISO: new Date(end).toISOString(),
      clamped: true
    })
  })

  it('clamps a late-July calendar pick that exceeds 30 days by afternoon on Aug 20', () => {
    const resolved = resolveOpsCustomTimeRange(
      '2026-07-21T00:00:00.000+08:00',
      '2026-08-20T13:37:00.000+08:00'
    )
    expect(resolved.ok).toBe(true)
    if (!resolved.ok) return
    expect(resolved.clamped).toBe(true)
    expect(resolved.endISO).toBe(new Date('2026-08-20T13:37:00.000+08:00').toISOString())
    expect(Date.parse(resolved.endISO) - Date.parse(resolved.startISO)).toBe(OPS_MAX_WINDOW_MS)
  })

  it('rejects inverted ranges', () => {
    expect(resolveOpsCustomTimeRange('2026-08-20T01:00:00.000Z', '2026-08-20T00:00:00.000Z')).toEqual({
      ok: false,
      error: 'inverted'
    })
  })

  it('rejects invalid timestamps', () => {
    expect(resolveOpsCustomTimeRange('not-a-date', '2026-08-20T00:00:00.000Z')).toEqual({
      ok: false,
      error: 'invalid'
    })
    expect(resolveOpsCustomTimeRange('2026-08-20T00:00:00.000Z', '')).toEqual({
      ok: false,
      error: 'invalid'
    })
  })
})

describe('datetime-local conversion', () => {
  it('round-trips a local Date through the datetime-local input value', () => {
    const local = new Date(2026, 7, 20, 13, 37)
    const value = toDatetimeLocalValue(local)
    expect(value).toBe('2026-08-20T13:37')
    expect(datetimeLocalToISO(value)).toBe(local.toISOString())
  })
})
