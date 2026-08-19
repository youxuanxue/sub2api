import { apiClient } from './client'

export type APIKeyCapabilityProtocol = 'anthropic' | 'openai' | 'gemini' | 'codex' | 'antigravity'
export type APIKeyCapabilityModality = 'chat' | 'embedding' | 'image' | 'video'

export interface APIKeyCapabilityGroup {
  id: number
  name: string
  platform: string
}

export interface APIKeyCapabilityRoute {
  protocol: APIKeyCapabilityProtocol
  modality: APIKeyCapabilityModality
  selected_group: APIKeyCapabilityGroup
}

export interface APIKeyCapabilityModel {
  id: string
  protocols: APIKeyCapabilityProtocol[]
  modalities: APIKeyCapabilityModality[]
  routes: APIKeyCapabilityRoute[]
  selected_group: APIKeyCapabilityGroup
}

export interface APIKeyCapabilitiesResponse {
  api_key_id: number
  routing_mode: 'direct' | 'universal'
  models: APIKeyCapabilityModel[]
}

export async function getAPIKeyCapabilities(
  apiKeyId: number,
  protocol?: APIKeyCapabilityProtocol,
): Promise<APIKeyCapabilitiesResponse> {
  const { data } = await apiClient.get<APIKeyCapabilitiesResponse>(
    `/me/api-keys/${apiKeyId}/capabilities`,
    { params: protocol ? { protocol } : undefined },
  )
  return data
}
