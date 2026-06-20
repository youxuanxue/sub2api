/**
 * TokenKey-only helper: normalize the per-model "additional" rate-limit windows
 * returned by the OpenAI/Codex `/backend-api/wham/usage` quota query so the admin
 * account card can render them.
 *
 * Background — why this exists
 * ----------------------------
 * A Codex (ChatGPT) account has TWO kinds of rate-limit window:
 *   1. The account-wide BASE window (`rate_limit.primary_window` / `secondary_window`,
 *      i.e. the general 5h / 7d limits) — already surfaced by AccountUsageCell.
 *   2. Per-model METERED windows (`additional_rate_limits[]`), e.g.
 *      "GPT-5.3-Codex-Spark", each with its OWN 5h / 7d sub-window.
 *
 * The two are independent: the spark 5h window can be 100% used (exhausted) while
 * the account-wide 5h window still reads ~4% used. When that happens the upstream
 * 429s codex-spark requests and TokenKey throttles the account, yet the card only
 * shows the healthy general window — operators see a throttled account with a 4%
 * gauge and conclude "刷不出 codex 的额度" (can't pull up codex's quota).
 *
 * The backend already fetches `additional_rate_limits` (OpenAIQuotaService.QueryUsage),
 * it was simply never rendered. This helper turns each additional limit into a pair
 * of display-ready windows (5h + 7d, with a resets-at ISO string) for UsageProgressBar.
 *
 * The 5h-vs-7d classification mirrors new-api's codex-usage dialog: classify each
 * window by `limit_window_seconds` (≤ 6h ⇒ the 5h bucket), then fall back to
 * primary = 5h / secondary = 7d when the durations are missing/ambiguous.
 */
import type {
  OpenAIAdditionalRateLimit,
  OpenAIQuotaUsage,
  OpenAIRateLimitWindow
} from '@/api/admin/accounts'

/** Any window whose declared size is ≤ this is treated as the short (5h) bucket. */
const FIVE_HOUR_MAX_SECONDS = 6 * 60 * 60

export interface NormalizedCodexLimitWindow {
  /** Consumed percent (0–100+), as returned by upstream `used_percent`. */
  usedPercent: number
  /** Window reset time as an ISO-8601 string, or null when unknown. */
  resetsAtIso: string | null
}

export interface NormalizedCodexAdditionalLimit {
  /** Raw upstream label, e.g. "GPT-5.3-Codex-Spark". May be empty. */
  name: string
  /** Upstream metered_feature key (used for a stable v-for key). */
  meteredFeature: string
  /** Upstream-declared limit_reached flag (any window currently blocking). */
  limitReached: boolean
  fiveHour: NormalizedCodexLimitWindow | null
  weekly: NormalizedCodexLimitWindow | null
}

/**
 * Convert a single upstream window into a display window. `reset_at` is upstream
 * unix SECONDS (×1000 for a JS Date); fall back to `reset_after_seconds` (relative).
 */
function toDisplayWindow(
  window: OpenAIRateLimitWindow | null | undefined
): NormalizedCodexLimitWindow | null {
  if (!window) return null

  const usedPercent = Number.isFinite(window.used_percent) ? window.used_percent : 0

  let resetsAtIso: string | null = null
  if (window.reset_at && window.reset_at > 0) {
    resetsAtIso = new Date(window.reset_at * 1000).toISOString()
  } else if (window.reset_after_seconds && window.reset_after_seconds > 0) {
    resetsAtIso = new Date(Date.now() + window.reset_after_seconds * 1000).toISOString()
  }

  return { usedPercent, resetsAtIso }
}

/**
 * Split the primary/secondary windows of one rate-limit envelope into the 5h and
 * 7d buckets. Classifies by `limit_window_seconds`, then falls back to the
 * conventional primary = 5h / secondary = 7d ordering for anything unclassified.
 */
function classifyWindows(
  primary: OpenAIRateLimitWindow | null | undefined,
  secondary: OpenAIRateLimitWindow | null | undefined
): { fiveHour: OpenAIRateLimitWindow | null; weekly: OpenAIRateLimitWindow | null } {
  const candidates = [primary, secondary].filter(Boolean) as OpenAIRateLimitWindow[]

  let fiveHour: OpenAIRateLimitWindow | null = null
  let weekly: OpenAIRateLimitWindow | null = null

  for (const window of candidates) {
    const seconds = window.limit_window_seconds
    if (seconds > 0 && seconds <= FIVE_HOUR_MAX_SECONDS) {
      if (!fiveHour) fiveHour = window
    } else if (seconds > FIVE_HOUR_MAX_SECONDS) {
      if (!weekly) weekly = window
    }
  }

  // Fall back to the conventional ordering for windows we could not classify by
  // duration (missing/zero limit_window_seconds, or both landed in one bucket).
  if (!fiveHour && !weekly) {
    return { fiveHour: primary ?? null, weekly: secondary ?? null }
  }
  if (!fiveHour) {
    fiveHour = candidates.find((window) => window !== weekly) ?? null
  }
  if (!weekly) {
    weekly = candidates.find((window) => window !== fiveHour) ?? null
  }

  return { fiveHour, weekly }
}

function toNormalizedLimit(
  item: OpenAIAdditionalRateLimit
): NormalizedCodexAdditionalLimit | null {
  const rateLimit = item.rate_limit ?? null
  const { fiveHour, weekly } = classifyWindows(
    rateLimit?.primary_window,
    rateLimit?.secondary_window
  )

  const fiveHourWindow = toDisplayWindow(fiveHour)
  const weeklyWindow = toDisplayWindow(weekly)
  if (!fiveHourWindow && !weeklyWindow) return null

  // Raw upstream label only — presentation (incl. any "unnamed limit" fallback
  // and i18n) belongs to the view, not this pure-data helper.
  return {
    name: (item.limit_name || item.metered_feature || '').trim(),
    meteredFeature: item.metered_feature || '',
    limitReached: rateLimit?.limit_reached ?? false,
    fiveHour: fiveHourWindow,
    weekly: weeklyWindow
  }
}

/**
 * Build the display list of per-model metered limits from a quota-usage payload.
 * Limits with no usable window data are dropped. Returns [] for missing input.
 */
export function normalizeCodexAdditionalLimits(
  usage: OpenAIQuotaUsage | null | undefined
): NormalizedCodexAdditionalLimit[] {
  const items = usage?.additional_rate_limits ?? []
  const result: NormalizedCodexAdditionalLimit[] = []
  for (const item of items) {
    const normalized = toNormalizedLimit(item)
    if (normalized) result.push(normalized)
  }
  return result
}
