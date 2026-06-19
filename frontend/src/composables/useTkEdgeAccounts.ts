/**
 * Edge Accounts overview composable (TokenKey-only).
 *
 * Owns all state + the API call for the read-only cross-edge account view, and
 * keeps the data live with the same auto-refresh engine the admin accounts page
 * uses (views/admin/AccountsView.vue): a 1s countdown tick, a configurable
 * interval (5/10/15/30s) persisted to localStorage, an enable toggle, ETag/304 so
 * an unchanged poll is a no-op, and an incremental edge merge so unchanged edge
 * cards keep their reference (no full-table flicker). Without auto-refresh the page
 * would only fetch once on mount and could show a stale snapshot — e.g. a
 * 5h-window utilization captured just after a window roll — diverging from the
 * always-fresh per-edge page. Mirrors the TK convention of useTk*.ts composables
 * (CLAUDE.md §5).
 */

import { ref, computed, onMounted, onBeforeUnmount } from 'vue'
import { useI18n } from 'vue-i18n'
import { useIntervalFn } from '@vueuse/core'
import { adminAPI } from '@/api/admin'
import { unknownToErrorMessage } from '@/utils/authError'
import type { EdgeAccountsResult } from '@/api/admin/edgeAccounts'
import {
  EDGE_STATUS_ALL,
  EDGE_GROUP_ALL,
  matchesStatusFilter,
  matchesGroupFilter,
  collectGroupNames
} from '@/utils/edgeAccounts.tk'

// Selectable auto-refresh cadences (seconds), matching the admin accounts page. The
// backend fronts the fan-out with a stale-while-revalidate cache + ETag, so even a
// 5s cadence is cheap (cache hit / 304) — no longer "hammering the fan-out".
export const EDGE_AUTO_REFRESH_INTERVALS = [5, 10, 15, 30] as const
export type EdgeAutoRefreshInterval = (typeof EDGE_AUTO_REFRESH_INTERVALS)[number]

const AUTO_REFRESH_STORAGE_KEY = 'edge-accounts-auto-refresh'
const DEFAULT_INTERVAL: EdgeAutoRefreshInterval = 30

interface PersistedPrefs {
  enabled: boolean
  interval: EdgeAutoRefreshInterval
}

function loadPrefs(): PersistedPrefs {
  // Default: enabled at 30s — preserve the page's prior always-fresh ops behavior.
  const fallback: PersistedPrefs = { enabled: true, interval: DEFAULT_INTERVAL }
  if (typeof localStorage === 'undefined') return fallback
  try {
    const raw = localStorage.getItem(AUTO_REFRESH_STORAGE_KEY)
    if (!raw) return fallback
    const parsed = JSON.parse(raw) as Partial<PersistedPrefs>
    const interval = EDGE_AUTO_REFRESH_INTERVALS.includes(parsed.interval as EdgeAutoRefreshInterval)
      ? (parsed.interval as EdgeAutoRefreshInterval)
      : DEFAULT_INTERVAL
    return { enabled: parsed.enabled !== false, interval }
  } catch {
    return fallback
  }
}

/**
 * Merge a freshly-fetched edges array into the current one, preserving the object
 * reference of any edge whose content is byte-identical so Vue does not re-render
 * its card. Edge payloads are small (≤ a few edges, ≤ a handful of accounts each),
 * so a per-edge JSON compare is cheap. Analog of AccountsView's
 * mergeAccountsIncrementally, at edge granularity.
 */
export function mergeEdges(current: EdgeAccountsResult[], next: EdgeAccountsResult[]): EdgeAccountsResult[] {
  const byId = new Map(current.map((e) => [e.edge_id, e]))
  let changed = next.length !== current.length
  const merged = next.map((n) => {
    const cur = byId.get(n.edge_id)
    if (cur && JSON.stringify(cur) === JSON.stringify(n)) return cur
    changed = true
    return n
  })
  if (!changed) {
    for (let i = 0; i < merged.length; i += 1) {
      if (merged[i].edge_id !== current[i]?.edge_id) {
        changed = true
        break
      }
    }
  }
  return changed ? merged : current
}

// 'all' is the sentinel the backend maps to an empty platform filter (every
// platform). The page defaults to it so the overview is complete; the filter
// narrows to a single platform.
export function useTkEdgeAccounts(initialPlatform = 'all') {
  const { t } = useI18n()
  const platform = ref(initialPlatform)
  const edges = ref<EdgeAccountsResult[]>([])
  const loading = ref(false)
  const error = ref<string | null>(null)
  const lastFetchedAt = ref<Date | null>(null)

  // Client-side account filters applied on top of the (platform-scoped) aggregate
  // the backend already returned — no extra fetch, the data carries every status
  // and group. Platform stays server-side (it changes which aggregate is cached/
  // fetched); status + group narrow the prod-side view in the browser.
  const statusFilter = ref(EDGE_STATUS_ALL)
  const groupFilter = ref(EDGE_GROUP_ALL)
  const accountFilterActive = computed(
    () => statusFilter.value !== EDGE_STATUS_ALL || groupFilter.value !== EDGE_GROUP_ALL
  )

  // edges with each reachable edge's accounts narrowed by the active status/group
  // filter. Fast path: with no account filter active, return the raw `edges` ref
  // unchanged so the mergeEdges reference-stability (no re-render on auto-refresh)
  // is fully preserved. Unreachable edges are kept verbatim (no accounts to
  // filter); reachable edges whose accounts all filter out are dropped only when a
  // filter is active, so an unfiltered view still shows reachable-but-empty edges.
  const displayEdges = computed<EdgeAccountsResult[]>(() => {
    if (!accountFilterActive.value) return edges.value
    const out: EdgeAccountsResult[] = []
    for (const e of edges.value) {
      if (!e.ok) {
        out.push(e)
        continue
      }
      const accounts = e.accounts.filter(
        (a) => matchesStatusFilter(a, statusFilter.value) && matchesGroupFilter(a, groupFilter.value)
      )
      if (accounts.length === 0) continue
      out.push({ ...e, accounts })
    }
    return out
  })

  // Group-name options for the 分组 dropdown, derived from the live aggregate
  // (reachable edges' accounts) so it never offers a stale/absent group.
  const groupOptions = computed(() => collectGroupNames(edges.value))

  const okEdges = computed(() => edges.value.filter((e) => e.ok))
  const failedEdges = computed(() => edges.value.filter((e) => !e.ok))
  // Account-level count reflects what is shown (post-filter); edge reachability
  // counts above stay on the raw aggregate.
  const totalAccounts = computed(() =>
    displayEdges.value.reduce((n, e) => (e.ok ? n + e.accounts.length : n), 0)
  )

  // Fleet-wide live-vs-capacity totals across all currently-schedulable accounts
  // on reachable edges. Each metric carries both the summed *current* live gauge
  // and the summed *configured* cap, so the header reads "current/capacity" — the
  // same shape AccountCapacityCell shows per account, just aggregated. Paused /
  // disabled / temp-unschedulable accounts are excluded (is_schedulable === false)
  // so the totals reflect only schedulable capacity. The platform filter is applied
  // upstream (the backend scopes `edges` to it) and the status/group filters are
  // applied here (displayEdges), so the totals always match the visible rows.
  //
  // Live gauges (current_concurrency / current_rpm / active_sessions) and their
  // caps only ever populate for the same accounts (e.g. RPM/sessions are
  // anthropic-oauth-only), so summing `?? 0` keeps current and capacity over an
  // identical account set — accounts without a metric contribute 0 to both sides.
  // rpm.sticky is Σ(base_rpm + rpm_sticky_buffer) — the effective RPM ceiling a
  // sticky-routed request may reach — mirroring AccountCapacityCell's rpm display.
  //
  // No utilization colouring on these aggregates: a fleet sum at 50% can still hide
  // an individual account pinned at its cap, so a green/red badge here would mislead.
  // The honest signal is the raw current/capacity pair; per-account hot spots stay
  // visible in each row's own coloured badge.
  const totals = computed(() => {
    let count = 0
    let concurrency = 0
    let curConcurrency = 0
    let baseRpm = 0
    let stickyRpm = 0
    let curRpm = 0
    let sessions = 0
    let curSessions = 0
    for (const e of displayEdges.value) {
      if (!e.ok) continue
      for (const a of e.accounts) {
        if (!a.is_schedulable) continue
        count++
        concurrency += a.concurrency ?? 0
        curConcurrency += a.current_concurrency ?? 0
        baseRpm += a.base_rpm ?? 0
        stickyRpm += (a.base_rpm ?? 0) + (a.rpm_sticky_buffer ?? 0)
        curRpm += a.current_rpm ?? 0
        sessions += a.max_sessions ?? 0
        curSessions += a.active_sessions ?? 0
      }
    }
    return {
      count,
      concurrency: { current: curConcurrency, max: concurrency },
      rpm: { current: curRpm, base: baseRpm, sticky: stickyRpm },
      sessions: { current: curSessions, max: sessions }
    }
  })

  // --- Auto-refresh engine (mirrors AccountsView.vue) ---
  const prefs = loadPrefs()
  const autoRefreshEnabled = ref(prefs.enabled)
  const autoRefreshIntervalSeconds = ref<EdgeAutoRefreshInterval>(prefs.interval)
  const autoRefreshCountdown = ref(0)
  const autoRefreshIntervals = EDGE_AUTO_REFRESH_INTERVALS
  const etag = ref<string | null>(null)
  const fetching = ref(false)

  function persistPrefs() {
    if (typeof localStorage === 'undefined') return
    try {
      localStorage.setItem(
        AUTO_REFRESH_STORAGE_KEY,
        JSON.stringify({ enabled: autoRefreshEnabled.value, interval: autoRefreshIntervalSeconds.value })
      )
    } catch {
      // ignore quota / privacy-mode write failures
    }
  }

  // Full (re)load: used on mount, manual refresh, and platform change. Replaces the
  // visible loading state and resets the ETag baseline.
  async function fetch() {
    if (loading.value) return
    loading.value = true
    error.value = null
    try {
      const res = await adminAPI.edgeAccounts.listWithEtag({ platform: platform.value })
      if (!res.notModified && res.data) {
        edges.value = res.data.edges ?? []
      }
      etag.value = res.etag
      lastFetchedAt.value = new Date()
    } catch (e: unknown) {
      // The axios response interceptor (api/client.ts) rejects with a *flattened
      // plain object* { status, code, message, ... }, NOT an Error instance — so
      // `String(e)` here rendered "[object Object]" in the error banner. Pull the
      // backend message off that shape via the shared helper, like the other TK
      // composables (useNewApiChannelTypes.ts) do.
      error.value = unknownToErrorMessage(e, t('admin.edgeAccounts.loadFailed'))
      edges.value = []
      etag.value = null
    } finally {
      loading.value = false
    }
  }

  // Background incremental refresh driven by the countdown. Skips the visible
  // loading spinner; on 304 it is a true no-op, on 200 it merges so unchanged edge
  // cards keep their reference.
  async function refreshIncrementally() {
    if (fetching.value || loading.value) return
    fetching.value = true
    try {
      const res = await adminAPI.edgeAccounts.listWithEtag(
        { platform: platform.value },
        { etag: etag.value }
      )
      if (res.etag) etag.value = res.etag
      if (!res.notModified && res.data) {
        edges.value = mergeEdges(edges.value, res.data.edges ?? [])
      }
      lastFetchedAt.value = new Date()
    } catch {
      // Transient auto-refresh failures keep the last-good view; the next tick retries.
    } finally {
      fetching.value = false
    }
  }

  // Switch the platform filter and immediately refetch (the auto-refresh would
  // otherwise leave the old platform's rows on screen for up to the interval).
  function setPlatform(p: string) {
    if (p === platform.value) return
    platform.value = p
    etag.value = null // different filter → different aggregate
    void fetch()
    if (autoRefreshEnabled.value) autoRefreshCountdown.value = autoRefreshIntervalSeconds.value
  }

  // Status / group filters are purely client-side over the already-fetched
  // aggregate: just set the ref and let displayEdges recompute — no refetch.
  function setStatusFilter(s: string) {
    statusFilter.value = s
  }
  function setGroupFilter(g: string) {
    groupFilter.value = g
  }

  // 1s countdown tick (mirrors AccountsView.vue): skip when disabled, tab hidden, or
  // a fetch is already in flight; when the countdown hits 0, refresh and reset it.
  const { pause, resume } = useIntervalFn(
    () => {
      if (!autoRefreshEnabled.value) return
      if (typeof document !== 'undefined' && document.hidden) return
      if (loading.value || fetching.value) return
      if (autoRefreshCountdown.value <= 0) {
        autoRefreshCountdown.value = autoRefreshIntervalSeconds.value
        void refreshIncrementally()
        return
      }
      autoRefreshCountdown.value -= 1
    },
    1000,
    { immediate: false }
  )

  function setAutoRefreshEnabled(enabled: boolean) {
    autoRefreshEnabled.value = enabled
    persistPrefs()
    if (enabled) {
      autoRefreshCountdown.value = autoRefreshIntervalSeconds.value
      resume()
    } else {
      pause()
      autoRefreshCountdown.value = 0
    }
  }

  function setAutoRefreshInterval(seconds: EdgeAutoRefreshInterval) {
    autoRefreshIntervalSeconds.value = seconds
    persistPrefs()
    if (autoRefreshEnabled.value) autoRefreshCountdown.value = seconds
  }

  onMounted(() => {
    void fetch()
    if (autoRefreshEnabled.value) {
      autoRefreshCountdown.value = autoRefreshIntervalSeconds.value
      resume()
    }
  })
  onBeforeUnmount(() => pause())

  return {
    platform,
    edges,
    displayEdges,
    loading,
    error,
    lastFetchedAt,
    okEdges,
    failedEdges,
    totalAccounts,
    totals,
    fetch,
    setPlatform,
    // client-side account filters
    statusFilter,
    groupFilter,
    groupOptions,
    accountFilterActive,
    setStatusFilter,
    setGroupFilter,
    // auto-refresh controls
    autoRefreshEnabled,
    autoRefreshIntervalSeconds,
    autoRefreshIntervals,
    autoRefreshCountdown,
    setAutoRefreshEnabled,
    setAutoRefreshInterval
  }
}
