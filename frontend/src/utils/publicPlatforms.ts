import type { PlatformDashboardStats } from '@/types'
import type { PlatformUsage } from '@/api/admin/dashboard'
import type { PlatformQuotaItem } from '@/api/admin/users'
import type {
  UserMonitorDetail,
  UserMonitorExtraModel,
  UserMonitorModelDetail,
  UserMonitorView,
  MonitorTimelinePoint,
} from '@/api/channelMonitor'
import type {
  MonitorHealth,
  MonitorMatrixBucket,
  MonitorMatrixRow,
  MonitorMetric,
  MonitorModelRow,
} from '@/api/channelMonitorV2'

export const PUBLIC_PLATFORM_ORDER = [
  'anthropic',
  'openai',
  'google',
  'newapi',
  'kiro',
  'grok',
  'composite',
] as const

const PUBLIC_PLATFORM_LABELS: Readonly<Record<string, string>> = {
  anthropic: 'Anthropic',
  openai: 'OpenAI',
  google: 'Google',
  newapi: 'Extension Engine',
  kiro: 'Kiro',
  grok: 'Grok',
  composite: 'Composite',
}

const PUBLIC_PLATFORM_INTERNAL_SOURCES: Readonly<Record<string, readonly string[]>> = {
  google: ['gemini', 'antigravity', 'google'],
}

export function normalizePublicPlatform(platform: string | null | undefined): string {
  const value = platform?.trim() ?? ''
  const normalized = value.toLowerCase()
  if (normalized === 'gemini' || normalized === 'antigravity' || normalized === 'google') return 'google'
  return value
}

export function getPublicPlatformLabel(platform: string | null | undefined): string {
  const canonical = normalizePublicPlatform(platform)
  if (!canonical) return '-'
  return PUBLIC_PLATFORM_LABELS[canonical] ?? canonical
}

export function expandPublicPlatforms(platforms: readonly string[]): string[] {
  const result: string[] = []
  const seen = new Set<string>()
  for (const platform of platforms) {
    const canonical = normalizePublicPlatform(platform)
    for (const internal of PUBLIC_PLATFORM_INTERNAL_SOURCES[canonical] ?? [canonical]) {
      if (!internal || seen.has(internal)) continue
      seen.add(internal)
      result.push(internal)
    }
  }
  return result
}

// Google uses the existing Gemini visual tokens. This helper is intentionally
// public-surface-only so admin execution-platform views stay diagnostic.
export function getPublicPlatformStyleKey(platform: string | null | undefined): string {
  const canonical = normalizePublicPlatform(platform)
  return canonical === 'google' ? 'gemini' : canonical
}

export function normalizePlatformDashboardStats(
  items: readonly PlatformDashboardStats[] | null | undefined,
): PlatformDashboardStats[] {
  const result: PlatformDashboardStats[] = []
  const index = new Map<string, number>()
  for (const source of items ?? []) {
    const platform = normalizePublicPlatform(source.platform)
    if (!platform) continue
    const existingIndex = index.get(platform)
    if (existingIndex !== undefined) {
      const target = result[existingIndex]
      target.total_requests += source.total_requests
      target.total_tokens += source.total_tokens
      target.total_actual_cost += source.total_actual_cost
      target.today_requests += source.today_requests
      target.today_tokens += source.today_tokens
      target.today_actual_cost += source.today_actual_cost
      continue
    }
    index.set(platform, result.length)
    result.push({ ...source, platform })
  }
  return result
}

export function normalizePlatformUsage(
  items: readonly PlatformUsage[] | null | undefined,
): PlatformUsage[] {
  const result: PlatformUsage[] = []
  const index = new Map<string, number>()
  for (const source of items ?? []) {
    const platform = normalizePublicPlatform(source.platform)
    if (!platform) continue
    const existingIndex = index.get(platform)
    if (existingIndex !== undefined) {
      result[existingIndex].today_actual_cost += source.today_actual_cost
      result[existingIndex].total_actual_cost += source.total_actual_cost
      continue
    }
    index.set(platform, result.length)
    result.push({ ...source, platform })
  }
  return result
}

/** Normalize quota payloads at the public UI boundary as a compatibility guard
 * for older servers that still return internal Gemini/Antigravity rows. */
export function normalizePublicPlatformQuotas(
  items: readonly PlatformQuotaItem[] | null | undefined,
): PlatformQuotaItem[] {
  const result: PlatformQuotaItem[] = []
  const index = new Map<string, number>()
  for (const source of items ?? []) {
    const platform = normalizePublicPlatform(source.platform)
    if (!platform) continue
    const existingIndex = index.get(platform)
    if (existingIndex === undefined) {
      index.set(platform, result.length)
      result.push({ ...source, platform: platform as PlatformQuotaItem['platform'] })
      continue
    }
    mergePublicQuota(result[existingIndex], source)
  }
  return result
}

function mergePublicQuota(target: PlatformQuotaItem, source: PlatformQuotaItem): void {
  for (const window of ['daily', 'weekly', 'monthly'] as const) {
    const usage = `${window}_usage_usd` as const
    const limit = `${window}_limit_usd` as const
    const reset = `${window}_window_resets_at` as const
    target[usage] += source[usage] ?? 0
    if (target[limit] == null || source[limit] == null) target[limit] = null
    else target[limit] += source[limit]
    if (target[reset] !== source[reset]) target[reset] = null
  }
}

/** Merge public V2 rows and aligned buckets after the API filter expands
 * Google back into its internal source platforms. */
export function normalizePublicMonitorMatrixRows(
  items: readonly MonitorMatrixRow[] | null | undefined,
): MonitorMatrixRow[] {
  const result: MonitorMatrixRow[] = []
  const index = new Map<string, number>()
  for (const source of items ?? []) {
    const row = cloneMonitorRow(source)
    row.platform = normalizePublicPlatform(source.platform)
    const key = monitorDimensionKey(row)
    const existingIndex = index.get(key)
    if (existingIndex === undefined) {
      index.set(key, result.length)
      result.push(row)
      continue
    }
    const target = result[existingIndex]
    target.metrics = mergeMonitorMetric(target.metrics, row.metrics)
    target.health = mergeMonitorHealth(target.health, row.health)
    target.buckets = mergeMonitorBuckets(target.buckets, row.buckets)
  }
  return result
}

export function normalizePublicMonitorModelRows(
  items: readonly MonitorModelRow[] | null | undefined,
): MonitorModelRow[] {
  const result: MonitorModelRow[] = []
  const index = new Map<string, number>()
  for (const source of items ?? []) {
    const row = { ...source, platform: normalizePublicPlatform(source.platform) }
    const key = `${row.platform}|${row.model}`
    const existingIndex = index.get(key)
    if (existingIndex === undefined) {
      index.set(key, result.length)
      result.push(row)
      continue
    }
    const target = result[existingIndex]
    target.metrics = mergeMonitorMetric(target.metrics, row.metrics)
    target.health = mergeMonitorHealth(target.health, row.health)
  }
  return result
}

export interface PublicUserMonitorView extends UserMonitorView {
  source_monitor_ids: number[]
}

/** Collapse legacy active-probe cards at the public boundary. Preserve every
 * internal monitor id for public drill-downs, while merging visible
 * availability/status data conservatively so a weaker source is never hidden. */
export function normalizePublicMonitorViews(
  items: readonly UserMonitorView[] | null | undefined,
): PublicUserMonitorView[] {
  const result: PublicUserMonitorView[] = []
  const index = new Map<string, number>()
  for (const source of items ?? []) {
    const row = {
      ...source,
      name: getPublicPlatformLabel(source.provider) === 'Google' ? 'Google' : source.name,
      extra_models: [...(source.extra_models ?? [])],
      timeline: [...(source.timeline ?? [])],
      source_monitor_ids: [source.id],
    }
    const key = `${normalizePublicPlatform(source.provider)}|${source.group_name ?? ''}|${source.primary_model}`
    const existingIndex = index.get(key)
    if (existingIndex === undefined) {
      index.set(key, result.length)
      result.push(row)
      continue
    }
    const target = result[existingIndex]
    if (!target.source_monitor_ids.includes(source.id)) target.source_monitor_ids.push(source.id)
    target.primary_status = worseMonitorStatus(target.primary_status, row.primary_status)
    target.primary_latency_ms = maxNullable(target.primary_latency_ms, row.primary_latency_ms)
    target.primary_ping_latency_ms = maxNullable(target.primary_ping_latency_ms, row.primary_ping_latency_ms)
    target.availability_7d = Math.min(target.availability_7d, row.availability_7d)
    target.extra_models = mergeExtraModels(target.extra_models, row.extra_models)
    target.timeline = mergeTimelines(target.timeline, row.timeline)
  }
  return result
}

/** Merge every internal detail feeding one public monitor card. Values that
 * communicate reliability use the conservative (least healthy) source. */
export function mergePublicMonitorDetails(
  details: readonly UserMonitorDetail[],
): UserMonitorDetail {
  const [first, ...rest] = details
  if (!first) throw new Error('Expected at least one monitor detail')

  const result: UserMonitorDetail = {
    ...first,
    models: first.models.map((model) => ({ ...model })),
  }
  const index = new Map(result.models.map((model, i) => [model.model, i]))
  for (const detail of rest) {
    for (const model of detail.models) {
      const existingIndex = index.get(model.model)
      if (existingIndex === undefined) {
        index.set(model.model, result.models.length)
        result.models.push({ ...model })
        continue
      }
      mergePublicMonitorModelDetail(result.models[existingIndex], model)
    }
  }
  return result
}

function mergePublicMonitorModelDetail(
  target: UserMonitorModelDetail,
  source: UserMonitorModelDetail,
): void {
  target.latest_status = worseMonitorStatus(target.latest_status, source.latest_status)
  target.latest_latency_ms = maxNullable(target.latest_latency_ms, source.latest_latency_ms)
  target.availability_7d = Math.min(target.availability_7d, source.availability_7d)
  target.availability_15d = Math.min(target.availability_15d, source.availability_15d)
  target.availability_30d = Math.min(target.availability_30d, source.availability_30d)
  target.avg_latency_7d_ms = maxNullable(target.avg_latency_7d_ms, source.avg_latency_7d_ms)
}

function worseMonitorStatus(left: UserMonitorView['primary_status'], right: UserMonitorView['primary_status']): UserMonitorView['primary_status'] {
  const rank: Record<string, number> = { error: 4, failed: 3, degraded: 2, operational: 1 }
  return (rank[left] ?? 0) >= (rank[right] ?? 0) ? left : right
}

function maxNullable(left: number | null, right: number | null): number | null {
  if (left == null) return right
  if (right == null) return left
  return Math.max(left, right)
}

function mergeExtraModels(left: UserMonitorExtraModel[], right: UserMonitorExtraModel[]): UserMonitorExtraModel[] {
  const result = left.map((model) => ({ ...model }))
  const index = new Map(result.map((model, i) => [model.model, i]))
  for (const model of right) {
    const existingIndex = index.get(model.model)
    if (existingIndex === undefined) {
      index.set(model.model, result.length)
      result.push({ ...model })
      continue
    }
    result[existingIndex].status = worseMonitorStatus(result[existingIndex].status, model.status)
    result[existingIndex].latency_ms = maxNullable(result[existingIndex].latency_ms, model.latency_ms)
  }
  return result
}

function mergeTimelines(left: MonitorTimelinePoint[], right: MonitorTimelinePoint[]): MonitorTimelinePoint[] {
  const result = left.map((point) => ({ ...point }))
  const index = new Map(result.map((point, i) => [point.checked_at, i]))
  for (const point of right) {
    const existingIndex = index.get(point.checked_at)
    if (existingIndex === undefined) {
      index.set(point.checked_at, result.length)
      result.push({ ...point })
      continue
    }
    result[existingIndex].status = worseMonitorStatus(result[existingIndex].status, point.status)
    result[existingIndex].latency_ms = maxNullable(result[existingIndex].latency_ms, point.latency_ms)
    result[existingIndex].ping_latency_ms = maxNullable(result[existingIndex].ping_latency_ms, point.ping_latency_ms)
  }
  return result.sort((a, b) => b.checked_at.localeCompare(a.checked_at))
}

function monitorDimensionKey(row: MonitorMatrixRow): string {
  return [row.platform, row.group_id ?? '', row.group_name ?? '', row.model ?? ''].join('|')
}

function cloneMonitorRow(row: MonitorMatrixRow): MonitorMatrixRow {
  return {
    ...row,
    metrics: { ...row.metrics, ttft: { ...row.metrics.ttft }, duration: { ...row.metrics.duration } },
    health: { ...row.health },
    buckets: (row.buckets ?? []).map((bucket) => ({
      ...bucket,
      metrics: { ...bucket.metrics, ttft: { ...bucket.metrics.ttft }, duration: { ...bucket.metrics.duration } },
      health: { ...bucket.health },
    })),
  }
}

function mergeMonitorBuckets(
  left: MonitorMatrixBucket[],
  right: MonitorMatrixBucket[],
): MonitorMatrixBucket[] {
  const result = left.map((bucket) => ({ ...bucket }))
  const index = new Map(result.map((bucket, i) => [bucket.bucket_start, i]))
  for (const bucket of right) {
    const existingIndex = index.get(bucket.bucket_start)
    if (existingIndex === undefined) {
      index.set(bucket.bucket_start, result.length)
      result.push(bucket)
      continue
    }
    result[existingIndex] = {
      ...result[existingIndex],
      metrics: mergeMonitorMetric(result[existingIndex].metrics, bucket.metrics),
      health: mergeMonitorHealth(result[existingIndex].health, bucket.health),
    }
  }
  return result.sort((a, b) => a.bucket_start.localeCompare(b.bucket_start))
}

function mergeMonitorMetric(left: MonitorMetric, right: MonitorMetric): MonitorMetric {
  const requestCount = left.request_count + right.request_count
  const cacheNumerator = left.cache_rate_numerator + right.cache_rate_numerator
  const cacheDenominator = left.cache_rate_denominator + right.cache_rate_denominator
  return {
    ...left,
    success_requests: left.success_requests + right.success_requests,
    error_requests: left.error_requests + right.error_requests,
    request_count: requestCount,
    token_count: left.token_count + right.token_count,
    rpm: left.rpm + right.rpm,
    tpm: left.tpm + right.tpm,
    error_rate: requestCount ? (left.error_requests + right.error_requests) / requestCount : 0,
    cache_rate: cacheDenominator ? cacheNumerator / cacheDenominator : 0,
    cache_rate_numerator: cacheNumerator,
    cache_rate_denominator: cacheDenominator,
    ttft: mergeLatency(left.ttft, right.ttft),
    duration: mergeLatency(left.duration, right.duration),
    upstream_affected_requests: (left.upstream_affected_requests ?? 0) + (right.upstream_affected_requests ?? 0),
    upstream_attempt_count: (left.upstream_attempt_count ?? 0) + (right.upstream_attempt_count ?? 0),
  }
}

function mergeLatency(left: MonitorMetric['ttft'], right: MonitorMetric['ttft']): MonitorMetric['ttft'] {
  const sampleCount = left.sample_count + right.sample_count
  const weighted = (a: number | null | undefined, b: number | null | undefined): number | null => {
    if (a == null && b == null) return null
    if (a == null) return b ?? null
    if (b == null) return a
    if (!sampleCount) return (a + b) / 2
    return (a * left.sample_count + b * right.sample_count) / sampleCount
  }
  return {
    sample_count: sampleCount,
    p50_ms: weighted(left.p50_ms, right.p50_ms),
    p90_ms: weighted(left.p90_ms, right.p90_ms),
    p95_ms: weighted(left.p95_ms, right.p95_ms),
    avg_ms: weighted(left.avg_ms, right.avg_ms),
  }
}

function mergeMonitorHealth(left: MonitorHealth, right: MonitorHealth): MonitorHealth {
  const leftScore = left.score
  const rightScore = right.score
  if (leftScore != null && rightScore != null) return leftScore <= rightScore ? left : right
  const rank: Record<string, number> = { critical: 3, warning: 2, healthy: 1, unknown: 0 }
  return (rank[left.overall] ?? 0) <= (rank[right.overall] ?? 0) ? left : right
}
