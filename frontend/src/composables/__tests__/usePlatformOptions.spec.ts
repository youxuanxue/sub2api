import { describe, expect, it } from 'vitest'
import { ref } from 'vue'

import { GATEWAY_PLATFORMS } from '@/constants/gatewayPlatforms'

import { PLATFORM_LABELS, usePlatformOptions } from '../usePlatformOptions'

describe('usePlatformOptions (US-017 regression — fifth platform newapi must surface in every admin picker)', () => {
  it('exposes exactly the 5 canonical gateway platforms in GATEWAY_PLATFORMS order', () => {
    const { options } = usePlatformOptions()

    expect(options.value).toHaveLength(GATEWAY_PLATFORMS.length)
    expect(options.value).toHaveLength(5)
    expect(options.value.map((o) => o.value)).toEqual([...GATEWAY_PLATFORMS])
  })

  it('includes newapi (the bug we are fixing — admin pickers used to drop it)', () => {
    const { options } = usePlatformOptions()
    const values = options.value.map((o) => o.value)

    expect(values).toContain('newapi')
    expect(options.value.find((o) => o.value === 'newapi')?.label).toBe('New API')
  })

  it('labels every platform with its brand name (no untranslated key leakage)', () => {
    const { options } = usePlatformOptions()

    for (const opt of options.value) {
      expect(opt.label).toBe(PLATFORM_LABELS[opt.value])
      expect(opt.label).not.toMatch(/^admin\./) // would indicate a stray i18n key
      expect(opt.label.length).toBeGreaterThan(0)
    }
  })

  it('optionsWithAll prepends the localized "all" sentinel and preserves order', () => {
    const { optionsWithAll } = usePlatformOptions()
    const opts = optionsWithAll('全部平台').value

    expect(opts[0]).toEqual({ value: '', label: '全部平台' })
    expect(opts.slice(1).map((o) => o.value)).toEqual([...GATEWAY_PLATFORMS])
    expect(opts).toHaveLength(GATEWAY_PLATFORMS.length + 1)
  })

  it('optionsWithAll re-evaluates the "all" label when a getter is passed (i18n reactivity)', () => {
    // Real-world bug guard: GroupsView passes `() => t('admin.groups.allPlatforms')`
    // so language switches must update the sentinel. A string snapshot would
    // freeze the original locale's label.
    const { optionsWithAll } = usePlatformOptions()
    const locale = ref<'en' | 'zh'>('en')
    const filterOpts = optionsWithAll(() =>
      locale.value === 'en' ? 'All Platforms' : '全部平台',
    )

    expect(filterOpts.value[0]).toEqual({ value: '', label: 'All Platforms' })
    locale.value = 'zh'
    expect(filterOpts.value[0]).toEqual({ value: '', label: '全部平台' })
  })

  it('optionsWithAll unwraps refs (MaybeRefOrGetter contract)', () => {
    const { optionsWithAll } = usePlatformOptions()
    const labelRef = ref('All')
    const filterOpts = optionsWithAll(labelRef)

    expect(filterOpts.value[0].label).toBe('All')
    labelRef.value = 'Tout'
    expect(filterOpts.value[0].label).toBe('Tout')
  })

  it('NEGATIVE — composable cannot return a platform absent from GATEWAY_PLATFORMS (drift guard)', () => {
    const { options } = usePlatformOptions()
    const allowed = new Set<string>(GATEWAY_PLATFORMS)

    for (const opt of options.value) {
      expect(allowed.has(opt.value)).toBe(true)
    }
  })

  it('NEGATIVE — adding a 6th platform to GATEWAY_PLATFORMS without a label entry would fail typecheck (compile-time guarantee documented in test)', () => {
    // PLATFORM_LABELS is typed as Record<AccountPlatform, string>; if a future
    // commit adds a 6th value to the AccountPlatform union and forgets to
    // populate PLATFORM_LABELS, `tsc` will fail. We assert the shape today so
    // the regression boundary is documented in a runnable test.
    expect(Object.keys(PLATFORM_LABELS).sort()).toEqual([...GATEWAY_PLATFORMS].sort())
  })
})
