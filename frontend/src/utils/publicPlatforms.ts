import type { PlatformDashboardStats } from '@/types'
import type { PlatformUsage } from '@/api/admin/dashboard'

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
