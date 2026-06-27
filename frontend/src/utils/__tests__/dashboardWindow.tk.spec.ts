import { describe, it, expect } from 'vitest'
import {
  rollingWindowTs,
  dashboardWindowParams,
  CALENDAR_PRESET_RANGE
} from '@/utils/dashboardWindow.tk'

describe('dashboardWindowParams', () => {
  it('returns absolute epoch-ms window for rolling presets', () => {
    const out = dashboardWindowParams('last24Hours') as {
      start_ts: number
      end_ts: number
    }
    expect(typeof out.start_ts).toBe('number')
    expect(typeof out.end_ts).toBe('number')
    expect(out.end_ts - out.start_ts).toBe(24 * 60 * 60 * 1000)
    // mirrors rollingWindowTs for rolling presets
    expect(rollingWindowTs('last24Hours')).not.toBeNull()
  })

  it('returns a canonical server-TZ range token for calendar presets', () => {
    expect(dashboardWindowParams('today')).toEqual({ range: 'today' })
    expect(dashboardWindowParams('yesterday')).toEqual({ range: 'yesterday' })
    expect(dashboardWindowParams('thisMonth')).toEqual({ range: 'this_month' })
    expect(dashboardWindowParams('lastMonth')).toEqual({ range: 'last_month' })
  })

  it('never emits start_ts/end_ts for calendar presets (those stay server-TZ)', () => {
    for (const preset of Object.keys(CALENDAR_PRESET_RANGE)) {
      const out = dashboardWindowParams(preset) as Record<string, unknown>
      expect(out).not.toHaveProperty('start_ts')
      expect(out).toHaveProperty('range')
    }
  })

  it('returns empty params for custom picks / unknown presets', () => {
    expect(dashboardWindowParams(null)).toEqual({})
    expect(dashboardWindowParams(undefined)).toEqual({})
    expect(dashboardWindowParams('custom')).toEqual({})
  })
})
