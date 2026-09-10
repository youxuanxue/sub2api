import { apiClient } from '../client'

export interface CursorAuthorization {
  id: string
  state: 'pending' | 'authorized' | 'claimed' | 'failed'
  authorization_url: string
  expires_at: string
  key_expires_at?: string
  email?: string
  models?: { id: string; displayName: string }[]
  error?: string
}
const path = '/admin/accounts/cursor'
export const cursorAPI = {
  async capabilities() {
    return (await apiClient.get<{ enabled: boolean }>(`${path}/capabilities`)).data
  },
  async start() {
    return (await apiClient.post<CursorAuthorization>(`${path}/authorizations`)).data
  },
  async status(id: string) {
    return (await apiClient.get<CursorAuthorization>(`${path}/authorizations/${encodeURIComponent(id)}`)).data
  },
  async cancel(id: string) {
    await apiClient.delete(`${path}/authorizations/${encodeURIComponent(id)}`)
  },
  async save(input: { session_id: string; name: string; group_ids: number[]; account_id?: number }, key: string) {
    return (await apiClient.post<{ id: number; name: string }>(`${path}/import`, input, {
      headers: { 'Idempotency-Key': key }
    })).data
  }
}
