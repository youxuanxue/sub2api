import type { Account, AccountUsageInfo, WindowStats } from '@/types'

export interface AccountUsageCellProps {
  account: Account
  todayStats?: WindowStats | null
  todayStatsLoading?: boolean
  manualRefreshToken?: number
  /** When provided (even null), skip self-fetch and render this usage verbatim. */
  usageOverride?: AccountUsageInfo | null
  /** Override active usage transport, e.g. for an account whose ID is edge-local. */
  activeUsageLoader?: () => Promise<AccountUsageInfo>
}

export const accountUsageCellPropDefaults = {
  todayStats: null,
  todayStatsLoading: false,
  manualRefreshToken: 0,
  usageOverride: undefined,
  activeUsageLoader: undefined
} as const
