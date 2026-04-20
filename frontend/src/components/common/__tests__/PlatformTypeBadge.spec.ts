/**
 * PlatformTypeBadge — US-017 regression guard.
 *
 * Why this spec exists: PlatformTypeBadge.vue used to default *every* unknown
 * platform string to "Gemini" with blue styling (lines 74-79 + 116-127 in the
 * pre-fix file). After the backend started returning a fifth platform `newapi`,
 * those rows were silently mislabeled as Gemini in the admin account list — the
 * exact symptom that triggered US-017. We pin the new behavior here so a future
 * refactor cannot bring the Gemini-fallback bug back.
 */
import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'

import PlatformTypeBadge from '../PlatformTypeBadge.vue'
import type { AccountPlatform, AccountType } from '@/types'

const i18n = createI18n({
  legacy: false,
  locale: 'en',
  messages: { en: {}, zh: {} },
})

function mountBadge(platform: AccountPlatform | string, type: AccountType = 'apikey') {
  return mount(PlatformTypeBadge, {
    props: { platform: platform as AccountPlatform, type },
    global: { plugins: [i18n] },
  })
}

describe('PlatformTypeBadge (US-017 — fifth platform newapi must not be mislabeled as Gemini)', () => {
  it('renders newapi as "New API" with cyan styling (the bug we are fixing)', () => {
    const wrapper = mountBadge('newapi')

    expect(wrapper.text()).toContain('New API')
    expect(wrapper.text()).not.toContain('Gemini')
    expect(wrapper.html()).toContain('bg-cyan-100')
    expect(wrapper.html()).not.toContain('bg-blue-100') // would indicate Gemini-fallback regression
  })

  it('NEGATIVE — truly unknown platforms fall back to neutral gray (no silent Gemini mislabel)', () => {
    const wrapper = mountBadge('totally-unknown-platform-x')

    expect(wrapper.text()).not.toContain('Gemini')
    expect(wrapper.text()).toContain('totally-unknown-platform-x')
    expect(wrapper.html()).toContain('bg-gray-100')
  })

  it('REGRESSION — the 4 historical platforms render with their canonical brand label and color', () => {
    const cases: Array<{ platform: AccountPlatform; label: string; color: string }> = [
      { platform: 'anthropic', label: 'Anthropic', color: 'bg-orange-100' },
      { platform: 'openai', label: 'OpenAI', color: 'bg-emerald-100' },
      { platform: 'gemini', label: 'Gemini', color: 'bg-blue-100' },
      { platform: 'antigravity', label: 'Antigravity', color: 'bg-purple-100' },
    ]

    for (const c of cases) {
      const wrapper = mountBadge(c.platform)
      expect(wrapper.text(), `${c.platform} label`).toContain(c.label)
      expect(wrapper.html(), `${c.platform} color`).toContain(c.color)
    }
  })

  it('renders the type label correctly across api types (apikey / oauth / setup-token / bedrock)', () => {
    expect(mountBadge('newapi', 'apikey').text()).toContain('Key')
    expect(mountBadge('anthropic', 'oauth').text()).toContain('OAuth')
    expect(mountBadge('anthropic', 'setup-token').text()).toContain('Token')
    expect(mountBadge('anthropic', 'bedrock').text()).toContain('AWS')
  })
})
