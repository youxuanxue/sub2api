import type { Account } from '@/types'
import { PLATFORM_KIRO } from '@/constants/gatewayPlatforms'

export function accountMatchesPlatformFilter(account: Account, platform: string): boolean {
  if (!platform) return true
  if (platform === PLATFORM_KIRO) {
    return account.platform === PLATFORM_KIRO
  }
  return account.platform === platform
}
