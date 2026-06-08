/**
 * TK: absolute-time windows for rolling dashboard presets.
 *
 * Rolling/duration presets ("近24小时 / 近7天 / …") are an absolute concept —
 * "the last N hours" is the same window everywhere on Earth. The legacy path
 * sends them as `YYYY-MM-DD` date strings, which the backend re-expands into the
 * *viewer's timezone* day boundaries. Two admins whose browsers report different
 * timezones (e.g. a fingerprint browser spoofing a US zone vs. Asia/Shanghai)
 * then see the SAME selection resolve to different absolute windows and thus
 * different numbers. To make the window timezone-independent, we send precise
 * epoch-millisecond instants (`start_ts`/`end_ts`) computed from `Date.now()`.
 *
 * Calendar presets (today / yesterday / this month / last month / custom date
 * pick) intentionally keep the date-string + timezone path: a "today" report is
 * a local-calendar concept and should follow the viewer's timezone.
 */

const HOUR_MS = 60 * 60 * 1000
const DAY_MS = 24 * HOUR_MS

/**
 * Maps a DateRangePicker preset value to its rolling duration in milliseconds.
 * Only presets listed here are treated as absolute rolling windows; any other
 * preset (or a manual custom selection, where preset is null) falls back to the
 * date-string + timezone path.
 */
export const ROLLING_PRESET_MS: Record<string, number> = {
  last24Hours: 24 * HOUR_MS,
  '7days': 7 * DAY_MS,
  '14days': 14 * DAY_MS,
  '30days': 30 * DAY_MS
}

/** Floor a timestamp to the start of its wall-clock minute. */
const floorToMinute = (ms: number): number => ms - (ms % (60 * 1000))

/**
 * For a rolling preset, returns the absolute window as epoch-ms params suitable
 * for spreading into a dashboard request. Returns null for calendar presets /
 * custom selections so the caller keeps sending only start_date/end_date.
 *
 * The end is floored to the current minute so near-simultaneous loads (and
 * multiple machines) produce an identical window — identical cache key, byte-
 * identical numbers. Refreshing a minute later simply advances by one minute of
 * fresh data, which is the intuitive behavior.
 */
export function rollingWindowTs(
  preset: string | null | undefined
): { start_ts: number; end_ts: number } | null {
  if (!preset) return null
  const duration = ROLLING_PRESET_MS[preset]
  if (!duration) return null

  const end = floorToMinute(Date.now())
  return { start_ts: end - duration, end_ts: end }
}
