/** Matches backend parseOpsTimeRange: end.Sub(start) > 30*24*time.Hour. */
export const OPS_MAX_WINDOW_MS = 30 * 24 * 60 * 60 * 1000

export type OpsCustomTimeRangeError = 'invalid' | 'inverted'

export type OpsResolvedCustomTimeRange =
  | { ok: false; error: OpsCustomTimeRangeError }
  | { ok: true; startISO: string; endISO: string; clamped: boolean }

export function resolveOpsCustomTimeRange(
  startISO: string | null | undefined,
  endISO: string | null | undefined
): OpsResolvedCustomTimeRange {
  const start = Date.parse(String(startISO || ''))
  const end = Date.parse(String(endISO || ''))
  if (!Number.isFinite(start) || !Number.isFinite(end)) return { ok: false, error: 'invalid' }
  if (start > end) return { ok: false, error: 'inverted' }
  if (end - start > OPS_MAX_WINDOW_MS) {
    return {
      ok: true,
      startISO: new Date(end - OPS_MAX_WINDOW_MS).toISOString(),
      endISO: new Date(end).toISOString(),
      clamped: true
    }
  }
  return {
    ok: true,
    startISO: new Date(start).toISOString(),
    endISO: new Date(end).toISOString(),
    clamped: false
  }
}

function pad2(n: number): string {
  return String(n).padStart(2, '0')
}

/** Format a Date as a `datetime-local` value in the viewer's local timezone. */
export function toDatetimeLocalValue(date: Date): string {
  return `${date.getFullYear()}-${pad2(date.getMonth() + 1)}-${pad2(date.getDate())}T${pad2(date.getHours())}:${pad2(date.getMinutes())}`
}

/** Parse a `datetime-local` value as local time and return UTC ISO-8601. */
export function datetimeLocalToISO(value: string): string {
  return new Date(value).toISOString()
}
