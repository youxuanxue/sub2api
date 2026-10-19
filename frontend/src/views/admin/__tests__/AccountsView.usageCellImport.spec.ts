import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

/**
 * Guards the blank 用量窗口 regression: AccountUsageCell must not be a
 * defineAsyncComponent of AccountsView while sharing accountUsageBatch helpers
 * with that view. Rollup otherwise emits AccountsView ↔ AccountUsageCell and
 * leaves usesLocalUsageWindows undefined at cell setup.
 */
describe('AccountsView AccountUsageCell import', () => {
  it('eagerly imports AccountUsageCell instead of defineAsyncComponent', () => {
    const source = readFileSync(
      resolve(__dirname, '../AccountsView.vue'),
      'utf8'
    )
    expect(source).toMatch(
      /import AccountUsageCell from '@\/components\/account\/AccountUsageCell\.vue'/
    )
    expect(source).not.toMatch(
      /defineAsyncComponent\(\(\)\s*=>\s*import\('@\/components\/account\/AccountUsageCell\.vue'\)\)/
    )
  })
})
