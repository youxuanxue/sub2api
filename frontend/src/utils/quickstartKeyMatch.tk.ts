import type { ApiKey } from '@/types'
import type { TkClientCatalogEntry } from '@/constants/clientIntegrations.tk'
import {
  PLATFORM_ANTHROPIC,
  PLATFORM_ANTIGRAVITY,
  PLATFORM_GEMINI,
  PLATFORM_GROK,
  PLATFORM_NEWAPI,
  PLATFORM_OPENAI,
} from '@/constants/gatewayPlatforms'
import { isUniversalKey } from '@/utils/studioUniversalKey.tk'

export type QuickstartKeyProtocol = 'anthropic' | 'openai' | 'gemini'

export interface QuickstartKeyMatchOptions {
  protocol?: 'anthropic' | 'openai'
  transport?: 'http' | 'websocket'
}

/** Protocols a key can speak on the wire. Mirrors QuickstartView.keyProtocols. */
export function keyProtocolsForApiKey(key: ApiKey): QuickstartKeyProtocol[] {
  if (key.routing_mode === 'universal') return ['anthropic', 'openai', 'gemini']
  const platform = key.group?.platform
  if (platform === PLATFORM_ANTHROPIC) return ['anthropic']
  if (platform === PLATFORM_OPENAI || platform === PLATFORM_NEWAPI || platform === PLATFORM_GROK) {
    const protocols: Array<'anthropic' | 'openai'> = ['openai']
    if (key.group?.allow_messages_dispatch) protocols.push('anthropic')
    return protocols
  }
  if (platform === PLATFORM_GEMINI) return ['gemini']
  if (platform === PLATFORM_ANTIGRAVITY) {
    const scopes = key.group?.supported_model_scopes ?? []
    return !scopes.length || scopes.includes('claude') ? ['anthropic', 'gemini'] : ['gemini']
  }
  return []
}

function requiredProtocols(
  client: TkClientCatalogEntry,
  options: QuickstartKeyMatchOptions,
): QuickstartKeyProtocol[] {
  if (client.id === 'qwen-code' && options.protocol) {
    return [options.protocol]
  }
  return client.protocols
}

function codexWebSocketAvailable(key: ApiKey): boolean {
  return key.routing_mode === 'universal' || key.group?.platform === PLATFORM_OPENAI
}

/** Human-facing reason when a key cannot drive the chosen client; empty = compatible. */
export function quickstartKeyDisabledReason(
  key: ApiKey,
  client: TkClientCatalogEntry,
  options: QuickstartKeyMatchOptions,
  t: (key: string) => string,
): string {
  if (!isKeyCompatibleWithClient(key, client, options)) {
    if (!key.group && key.routing_mode !== 'universal') {
      return t('quickstart.unavailableNoGroup')
    }
    if (key.group?.claude_code_only && client.id !== 'claude-code') {
      return t('quickstart.unavailableClaudeCodeOnly')
    }
    if (client.id === 'codex-cli' && options.transport === 'websocket' && !codexWebSocketAvailable(key)) {
      return t('quickstart.websocketUnavailable')
    }
    return t('quickstart.unavailableProtocol')
  }
  return ''
}

export function isKeyCompatibleWithClient(
  key: ApiKey,
  client: TkClientCatalogEntry,
  options: QuickstartKeyMatchOptions = {},
): boolean {
  if (!key.group && key.routing_mode !== 'universal') return false
  if (key.group?.claude_code_only && client.id !== 'claude-code') return false
  const available = keyProtocolsForApiKey(key)
  const required = requiredProtocols(client, options)
  if (!required.some((protocol) => available.includes(protocol))) return false
  if (client.id === 'codex-cli' && options.transport === 'websocket' && !codexWebSocketAvailable(key)) {
    return false
  }
  return true
}

function keyMatchScore(key: ApiKey, client: TkClientCatalogEntry): number {
  let score = 0
  if (isUniversalKey(key)) score += 40
  if (key.name?.toLowerCase() === 'trial') score += 30
  if (key.name?.toLowerCase() === 'quick start') score += 20
  if (client.id === 'claude-code' && key.group?.platform === PLATFORM_ANTHROPIC) score += 50
  if (client.id === 'codex-cli' && key.group?.platform === PLATFORM_OPENAI) score += 50
  if (client.id === 'gemini-cli' && (key.group?.platform === PLATFORM_GEMINI || key.group?.platform === PLATFORM_ANTIGRAVITY)) {
    score += 50
  }
  if (client.protocols.includes('openai') && key.group?.platform === PLATFORM_OPENAI) score += 25
  if (client.protocols.includes('anthropic') && key.group?.platform === PLATFORM_ANTHROPIC) score += 25
  if (client.protocols.includes('gemini') && key.group?.platform === PLATFORM_GEMINI) score += 25
  if (key.last_used_at) score += 5
  return score
}

/** Pick the best active key for the selected client; null when none fit. */
export function recommendKeyForClient(
  keys: ApiKey[],
  client: TkClientCatalogEntry,
  options: QuickstartKeyMatchOptions = {},
): ApiKey | null {
  const compatible = keys.filter((key) => isKeyCompatibleWithClient(key, client, options))
  if (!compatible.length) return null
  return compatible
    .slice()
    .sort((a, b) => keyMatchScore(b, client) - keyMatchScore(a, client))[0]
}
