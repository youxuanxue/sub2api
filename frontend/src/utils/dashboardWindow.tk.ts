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
 * Calendar presets (today / yesterday / this month / last month) are a
 * billing-day concept: the customer-facing dashboard ("今日消费"), quota resets,
 * and the daily rollup buckets are all anchored to the server/reporting
 * timezone. Following the *viewer's* browser timezone here made the admin "今天"
 * window refer to a different calendar day than the customer's own dashboard
 * whenever the operator's browser timezone differed from the server, so the two
 * surfaces reported different totals for the same user/day. We therefore send a
 * named `range` for these presets and let the backend resolve it in the server
 * timezone (see backend dashboard_handler_tk_window.go) — one canonical "today"
 * for every viewer. A manual custom date pick (preset === null) still flows
 * through the start_date/end_date path, parsed in the server-configured timezone
 * on the backend.
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

/**
 * Maps a DateRangePicker calendar preset to the backend's canonical `range`
 * token. The backend resolves these in the server/billing timezone so the window
 * is identical for every viewer (and matches the customer dashboard). Rolling
 * presets and manual custom picks are absent here — they keep their own paths.
 */
export const CALENDAR_PRESET_RANGE: Record<string, string> = {
  today: 'today',
  yesterday: 'yesterday',
  thisMonth: 'this_month',
  lastMonth: 'last_month'
}

/**
 * Returns the window params to spread into a dashboard request for the active
 * preset:
 *   - rolling preset  → { start_ts, end_ts }  (absolute, timezone-independent)
 *   - calendar preset → { range }             (server-TZ canonical, set on the
 *                                               backend)
 *   - custom pick / unknown → {}              (caller still sends start_date /
 *                                               end_date)
 */
export function dashboardWindowParams(
  preset: string | null | undefined
): { start_ts: number; end_ts: number } | { range: string } | Record<string, never> {
  const rolling = rollingWindowTs(preset)
  if (rolling) return rolling
  if (preset && CALENDAR_PRESET_RANGE[preset]) {
    return { range: CALENDAR_PRESET_RANGE[preset] }
  }
  return {}
}
