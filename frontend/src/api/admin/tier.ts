/**
 * Admin Tier (anthropic-oauth stability tier) API endpoints.
 *
 * Tiers are the reference-table projection of the git baseline
 * (backend/internal/baseline/anthropic-oauth-stability-baselines-tiered.json).
 * Accounts reference a tier by id; editing a tier row fans out to every
 * referencing account via Redis pub/sub. UI edits here are emergency/local —
 * the ops/anthropic pipeline re-asserts the rows from git on the next run.
 *
 * Mirrors api/admin/tlsFingerprintProfile.ts (CLAUDE.md §5: TK-only surface in
 * a dedicated module).
 */

import { apiClient } from '../client'

/**
 * Tier interface — matches backend model.Tier JSON shape.
 */
export interface Tier {
  id: number
  name: string
  description: string | null
  concurrency: number
  priority: number
  rate_multiplier: number
  base_rpm: number
  max_sessions: number
  rpm_sticky_buffer: number
  session_idle_timeout_minutes: number
  cache_ttl_override_enabled: boolean
  cache_ttl_override_target: string | null
  tls_profile_name: string | null
  tls_profile_id: number | null
  created_at: string
  updated_at: string
}

/**
 * Create/Update tier request body (full field set, matches backend tierRequest).
 */
export interface TierRequest {
  name: string
  description?: string | null
  concurrency: number
  priority: number
  rate_multiplier: number
  base_rpm: number
  max_sessions: number
  rpm_sticky_buffer: number
  session_idle_timeout_minutes: number
  cache_ttl_override_enabled: boolean
  cache_ttl_override_target?: string | null
  tls_profile_name?: string | null
  tls_profile_id?: number | null
}

export async function list(): Promise<Tier[]> {
  const { data } = await apiClient.get<Tier[]>('/admin/tiers')
  return data
}

export async function getById(id: number): Promise<Tier> {
  const { data } = await apiClient.get<Tier>(`/admin/tiers/${id}`)
  return data
}

export async function create(tierData: TierRequest): Promise<Tier> {
  const { data } = await apiClient.post<Tier>('/admin/tiers', tierData)
  return data
}

export async function update(id: number, updates: TierRequest): Promise<Tier> {
  const { data } = await apiClient.put<Tier>(`/admin/tiers/${id}`, updates)
  return data
}

export async function deleteTier(id: number): Promise<{ message: string }> {
  const { data } = await apiClient.delete<{ message: string }>(`/admin/tiers/${id}`)
  return data
}

export const tierAPI = {
  list,
  getById,
  create,
  update,
  delete: deleteTier
}

export default tierAPI
