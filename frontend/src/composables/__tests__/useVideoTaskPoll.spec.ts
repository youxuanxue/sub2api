import { describe, expect, it } from 'vitest'
import { pollIntervalMs } from '../useVideoTaskPoll'

describe('pollIntervalMs stepped backoff', () => {
  it('polls fast (5s) for the first minute', () => {
    expect(pollIntervalMs(0)).toBe(5_000)
    expect(pollIntervalMs(59_999)).toBe(5_000)
  })

  it('backs off to 10s after one minute', () => {
    expect(pollIntervalMs(60_000)).toBe(10_000)
    expect(pollIntervalMs(179_999)).toBe(10_000)
  })

  it('backs off to 15s after three minutes (and stays there — no hard cap)', () => {
    expect(pollIntervalMs(180_000)).toBe(15_000)
    expect(pollIntervalMs(60 * 60_000)).toBe(15_000) // an hour-old reattached task still polls
  })
})
