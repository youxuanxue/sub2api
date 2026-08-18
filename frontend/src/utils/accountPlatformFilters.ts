import type { Account } from '@/types'
import { PLATFORM_ANTHROPIC, PLATFORM_KIRO } from '@/constants/gatewayPlatforms'

const ACCOUNT_EDGE_BASE_URL_PATTERN = /^https:\/\/api-[a-z0-9]+\.tokenkey\.dev\/?$/

export function isKiroRelayStubAccount(account: Account): boolean {
  const baseUrl = typeof account.credentials?.base_url === 'string' ? account.credentials.base_url.trim() : ''
  const mirrorPlatform =
    typeof account.credentials?.mirror_platform === 'string'
      ? account.credentials.mirror_platform.trim().toLowerCase()
      : ''
  return (
    account.platform === PLATFORM_ANTHROPIC &&
    account.type === 'apikey' &&
    mirrorPlatform === PLATFORM_KIRO &&
    ACCOUNT_EDGE_BASE_URL_PATTERN.test(baseUrl)
  )
}

export function accountMatchesPlatformFilter(account: Account, platform: string): boolean {
  if (!platform) return true
  if (platform === PLATFORM_KIRO) {
    return account.platform === PLATFORM_KIRO || isKiroRelayStubAccount(account)
  }
  if (platform === PLATFORM_ANTHROPIC) {
    return account.platform === PLATFORM_ANTHROPIC && !isKiroRelayStubAccount(account)
  }
  return account.platform === platform
}
