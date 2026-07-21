/**
 * Admin API Keys API endpoints
 * Handles API key management for administrators
 */

import { apiClient } from '../client'
import type { ApiKey } from '@/types'

export interface UpdateApiKeyGroupResult {
  api_key: ApiKey
  auto_granted_group_access: boolean
  granted_group_id?: number
  granted_group_name?: string
}

/**
 * Update an API key's group binding
 * @param id - API Key ID
 * @param groupId - Group ID (0 to unbind, positive to bind, null/undefined to skip)
 * @returns Updated API key with auto-grant info
 */
export async function updateApiKeyGroup(id: number, groupId: number | null): Promise<UpdateApiKeyGroupResult> {
  const { data } = await apiClient.put<UpdateApiKeyGroupResult>(`/admin/api-keys/${id}`, {
    group_id: groupId === null ? 0 : groupId
  })
  return data
}

export interface CreateUserApiKeyRequest {
  name: string
  group_id?: number | null
  routing_mode?: 'direct' | 'universal'
  custom_key?: string
  quota?: number
  expires_in_days?: number
  rate_limit_5h?: number
  rate_limit_1d?: number
  rate_limit_7d?: number
}

/**
 * Issue a new API key for a user (admin-only).
 */
export async function createUserApiKey(userId: number, payload: CreateUserApiKeyRequest): Promise<ApiKey> {
  const { data } = await apiClient.post<ApiKey>(`/admin/users/${userId}/api-keys`, payload)
  return data
}

export const apiKeysAPI = {
  updateApiKeyGroup,
  createUserApiKey
}

export default apiKeysAPI
