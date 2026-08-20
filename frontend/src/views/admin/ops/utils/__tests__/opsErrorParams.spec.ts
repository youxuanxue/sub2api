import { describe, expect, it } from 'vitest'
import { buildOpsErrorTimeParams } from '../opsErrorParams'

describe('buildOpsErrorTimeParams', () => {
  it('uses explicit timestamps for a complete custom range', () => {
    expect(buildOpsErrorTimeParams('custom', '2026-08-01T00:00:00Z', '2026-08-02T00:00:00Z')).toEqual({
      start_time: '2026-08-01T00:00:00.000Z',
      end_time: '2026-08-02T00:00:00.000Z'
    })
  })

  it('clamps an oversized custom range to the max 30-day window', () => {
    expect(
      buildOpsErrorTimeParams('custom', '2026-07-21T00:00:00.000+08:00', '2026-08-20T13:37:00.000+08:00')
    ).toEqual({
      start_time: new Date(Date.parse('2026-08-20T13:37:00.000+08:00') - 30 * 24 * 60 * 60 * 1000).toISOString(),
      end_time: new Date('2026-08-20T13:37:00.000+08:00').toISOString()
    })
  })

  it('preserves predefined ranges and falls back for incomplete custom ranges', () => {
    expect(buildOpsErrorTimeParams('24h')).toEqual({ time_range: '24h' })
    expect(buildOpsErrorTimeParams('custom', null, null)).toEqual({ time_range: '1h' })
  })
})
