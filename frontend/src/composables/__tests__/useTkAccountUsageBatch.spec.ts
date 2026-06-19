import { describe, expect, it, vi, beforeEach } from 'vitest'

const { getBatchPassiveUsage } = vi.hoisted(() => ({ getBatchPassiveUsage: vi.fn() }))

vi.mock('@/api', () => ({
  adminAPI: {
    accounts: {
      getBatchPassiveUsage
    }
  }
}))

import { useTkAccountUsageBatch } from '../useTkAccountUsageBatch'
import type { Account, AccountUsageInfo } from '@/types'

function acct(id: number, platform: string, type: string): Account {
  return { id, platform, type } as unknown as Account
}

const usageA: AccountUsageInfo = { source: 'passive' } as AccountUsageInfo

describe('useTkAccountUsageBatch', () => {
  beforeEach(() => {
    getBatchPassiveUsage.mockReset()
  })

  it('only batches Anthropic OAuth/SetupToken rows; others get undefined override', async () => {
    getBatchPassiveUsage.mockResolvedValue({ usage: { '1': usageA } })
    const { refreshUsageBatch, usageOverrideFor } = useTkAccountUsageBatch()

    const accounts = [
      acct(1, 'anthropic', 'oauth'),
      acct(2, 'anthropic', 'setup-token'),
      acct(3, 'anthropic', 'apikey'), // not passive-capable
      acct(4, 'gemini', 'oauth'), // self-fetches, no override
      acct(5, 'openai', 'oauth'), // active path, no override
      acct(6, 'antigravity', 'oauth') // active path, no override
    ]

    await refreshUsageBatch(accounts)

    // Only the two batch-capable IDs are sent upstream.
    expect(getBatchPassiveUsage).toHaveBeenCalledTimes(1)
    expect(getBatchPassiveUsage).toHaveBeenCalledWith([1, 2])

    // Capable rows: override defined (suppresses self-fetch). id 1 has data,
    // id 2 omitted by server => null (still suppresses, shows "-").
    expect(usageOverrideFor(accounts[0])).toEqual(usageA)
    expect(usageOverrideFor(accounts[1])).toBeNull()

    // Non-capable rows: undefined => cell self-fetches as before.
    expect(usageOverrideFor(accounts[2])).toBeUndefined()
    expect(usageOverrideFor(accounts[3])).toBeUndefined()
    expect(usageOverrideFor(accounts[4])).toBeUndefined()
    expect(usageOverrideFor(accounts[5])).toBeUndefined()
  })

  it('returns null override for a capable row before the batch resolves', () => {
    const { usageOverrideFor } = useTkAccountUsageBatch()
    // No refresh yet: a capable row is still null (defined), never undefined,
    // so the cell does not self-fetch.
    expect(usageOverrideFor(acct(9, 'anthropic', 'oauth'))).toBeNull()
  })

  it('does not call the API when there are no capable rows', async () => {
    const { refreshUsageBatch } = useTkAccountUsageBatch()
    await refreshUsageBatch([acct(1, 'gemini', 'oauth'), acct(2, 'openai', 'oauth')])
    expect(getBatchPassiveUsage).not.toHaveBeenCalled()
  })

  it('ignores a stale response when a newer refresh has started (race guard)', async () => {
    const slow: AccountUsageInfo = { source: 'passive', seven_day: null } as AccountUsageInfo
    const fast: AccountUsageInfo = { source: 'passive', five_hour: null } as AccountUsageInfo

    let resolveSlow!: (v: { usage: Record<string, AccountUsageInfo> }) => void
    const slowPromise = new Promise<{ usage: Record<string, AccountUsageInfo> }>((r) => { resolveSlow = r })

    getBatchPassiveUsage
      .mockReturnValueOnce(slowPromise) // first (stale) call
      .mockResolvedValueOnce({ usage: { '1': fast } }) // second (fresh) call

    const { refreshUsageBatch, usageOverrideFor } = useTkAccountUsageBatch()
    const accounts = [acct(1, 'anthropic', 'oauth')]

    const p1 = refreshUsageBatch(accounts) // starts, will resolve later
    const p2 = refreshUsageBatch(accounts) // newer; resolves immediately
    await p2

    expect(usageOverrideFor(accounts[0])).toEqual(fast)

    // Now let the stale one resolve — it must NOT clobber the fresh value.
    resolveSlow({ usage: { '1': slow } })
    await p1
    expect(usageOverrideFor(accounts[0])).toEqual(fast)
  })

  it('failure-open: API error leaves the override map unchanged', async () => {
    getBatchPassiveUsage.mockResolvedValueOnce({ usage: { '1': usageA } })
    const { refreshUsageBatch, usageOverrideFor } = useTkAccountUsageBatch()
    const accounts = [acct(1, 'anthropic', 'oauth')]
    await refreshUsageBatch(accounts)
    expect(usageOverrideFor(accounts[0])).toEqual(usageA)

    getBatchPassiveUsage.mockRejectedValueOnce(new Error('boom'))
    await refreshUsageBatch(accounts)
    // Previous value retained (failure-open), not wiped to null.
    expect(usageOverrideFor(accounts[0])).toEqual(usageA)
  })
})
