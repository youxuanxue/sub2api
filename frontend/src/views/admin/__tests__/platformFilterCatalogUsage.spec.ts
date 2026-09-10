import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

function readSource(path: string): string {
  return readFileSync(resolve(path), 'utf8')
}

describe('admin platform filters', () => {
  it('uses the shared platform options on the subscriptions page', () => {
    const source = readSource('src/views/admin/SubscriptionsView.vue')
    expect(source).toContain("import { usePlatformOptions } from '@/composables/usePlatformOptions'")
    expect(source).toContain('const { optionsWithAll } = usePlatformOptions()')
    expect(source).toContain('const platformFilterOptions = optionsWithAll(')
  })

  it('uses the shared catalogs on the groups page', () => {
    const source = readSource('src/views/admin/GroupsView.vue')
    expect(source).toContain('...GROUP_PLATFORM_OPTIONS')
    expect(source).toContain('...CONCRETE_PLATFORM_OPTIONS')
  })

  it('uses the registry-backed platform catalog across account and ops filters', () => {
    for (const path of [
      'src/components/admin/account/AccountTableFilters.vue',
      'src/components/admin/ErrorPassthroughRulesModal.vue',
      'src/views/admin/ops/components/OpsDashboardHeader.vue'
    ]) {
      const source = readSource(path)
      expect(source).toContain("import { usePlatformOptions } from '@/composables/usePlatformOptions'")
      expect(source).toContain('= usePlatformOptions()')
    }
  })
})
