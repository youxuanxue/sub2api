import { describe, expect, it } from 'vitest'
import type { ApiKey } from '@/types'
import { TK_QUICKSTART_CLIENTS } from '@/constants/clientIntegrations.tk'
import {
  isKeyCompatibleWithClient,
  keyProtocolsForApiKey,
  recommendKeyForClient,
} from '@/utils/quickstartKeyMatch.tk'

const claudeClient = TK_QUICKSTART_CLIENTS.find((client) => client.id === 'claude-code')!
const codexClient = TK_QUICKSTART_CLIENTS.find((client) => client.id === 'codex-cli')!

const universalKey = (): ApiKey => ({
  id: 1,
  user_id: 1,
  key: 'sk-universal',
  name: 'Universal',
  group_id: null,
  routing_mode: 'universal',
  status: 'active',
  ip_whitelist: [],
  ip_blacklist: [],
  last_used_at: null,
  quota: 0,
  quota_used: 0,
  expires_at: null,
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
  rate_limit_5h: 0,
  rate_limit_1d: 0,
  rate_limit_7d: 0,
  usage_5h: 0,
  usage_1d: 0,
  usage_7d: 0,
  window_5h_start: null,
  window_1d_start: null,
  window_7d_start: null,
  reset_5h_at: null,
  reset_1d_at: null,
  reset_7d_at: null,
})

const anthropicKey = (): ApiKey => ({
  ...universalKey(),
  id: 2,
  name: 'Claude',
  routing_mode: 'direct',
  group_id: 10,
  group: {
    id: 10,
    name: 'Anthropic',
    platform: 'anthropic',
    claude_code_only: false,
  } as ApiKey['group'],
})

const openaiKey = (): ApiKey => ({
  ...universalKey(),
  id: 3,
  name: 'OpenAI',
  routing_mode: 'direct',
  group_id: 11,
  group: {
    id: 11,
    name: 'OpenAI',
    platform: 'openai',
  } as ApiKey['group'],
})

describe('quickstartKeyMatch', () => {
  it('derives anthropic-only protocols for direct anthropic keys', () => {
    expect(keyProtocolsForApiKey(anthropicKey())).toEqual(['anthropic'])
  })

  it('matches codex to openai keys and universal keys', () => {
    expect(isKeyCompatibleWithClient(openaiKey(), codexClient)).toBe(true)
    expect(isKeyCompatibleWithClient(universalKey(), codexClient)).toBe(true)
    expect(isKeyCompatibleWithClient(anthropicKey(), codexClient)).toBe(false)
  })

  it('prefers platform-matching keys over universal keys for codex', () => {
    const recommended = recommendKeyForClient([universalKey(), openaiKey(), anthropicKey()], codexClient)
    expect(recommended?.id).toBe(3)
  })

  it('prefers anthropic keys for claude-code when both exist', () => {
    const recommended = recommendKeyForClient([universalKey(), anthropicKey(), openaiKey()], claudeClient)
    expect(recommended?.id).toBe(2)
  })
})
