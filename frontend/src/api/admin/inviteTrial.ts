/**
 * Admin Invite-to-Trial API — one-step batch user provisioning that returns
 * ready-to-paste credential cards, plus reusable "试用方案" (trial preset) CRUD.
 *
 * TK-only surface in a dedicated module (CLAUDE.md §5). Backend:
 * backend/internal/handler/admin/user_handler_tk_provision.go.
 */

import { apiClient } from '../client'

/** A reusable trial preset (matches backend service.TrialPreset JSON shape). */
export interface TrialPreset {
  name: string
  group_id: number
  validity_days: number
  balance: number
  concurrency: number
  rpm_limit: number
  rate?: number | null
}

/** The effective plan applied to every provisioned user. */
export interface TrialPlan {
  group_id: number
  validity_days: number
  balance: number
  concurrency: number
  rpm_limit: number
  rate?: number | null
}

export interface TrialRecipient {
  email?: string
  password?: string
}

export interface InviteTrialRequest {
  preset_name?: string
  plan?: TrialPlan
  recipients?: TrialRecipient[]
  auto_count?: number
  issue_key?: boolean
  key_name?: string
}

/** Per-user provisioning result, carrying one-time credentials + the card text. */
export interface TrialCredential {
  user_id: number
  email: string
  password: string
  api_key?: string
  home_url: string
  group_id: number
  group_name: string
  balance: number
  expires_at?: string
  card_text: string
  error?: string
}

/** Provision a batch of trial users; returns one credential card per recipient. */
export async function inviteTrial(request: InviteTrialRequest): Promise<TrialCredential[]> {
  const { data } = await apiClient.post<{ results: TrialCredential[]; count: number }>(
    '/admin/users/invite-trial',
    request
  )
  return data.results
}

/** Read the saved trial presets. */
export async function getPresets(): Promise<TrialPreset[]> {
  const { data } = await apiClient.get<{ presets: TrialPreset[] }>('/admin/users/trial-presets')
  return data.presets
}

/** Replace the saved trial presets. */
export async function setPresets(presets: TrialPreset[]): Promise<TrialPreset[]> {
  const { data } = await apiClient.put<{ presets: TrialPreset[] }>('/admin/users/trial-presets', {
    presets
  })
  return data.presets
}

export default { inviteTrial, getPresets, setPresets }
