import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key
    })
  }
})

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showInfo: vi.fn()
  })
}))

import ModelWhitelistSelector from '../ModelWhitelistSelector.vue'

describe('ModelWhitelistSelector', () => {
  it('normalizes #128 model objects and renders pricing status badges', () => {
    const wrapper = mount(ModelWhitelistSelector, {
      props: {
        modelValue: [
          { id: 'claude-opus-4-6', pricing_status: 'priced' },
          { id: 'claude-sonnet-4-6', pricing_status: 'missing' },
        ],
        platform: 'newapi',
        pricingStatusByModel: {
          'claude-opus-4-6': 'priced',
          'claude-sonnet-4-6': 'missing',
        },
      },
      global: {
        stubs: {
          Icon: true,
        },
      },
    })

    expect(wrapper.text()).toContain('claude-opus-4-6')
    expect(wrapper.text()).toContain('claude-sonnet-4-6')
    expect(wrapper.text()).toContain('admin.accounts.newApiPlatform.pricingStatusPriced')
    expect(wrapper.text()).toContain('admin.accounts.newApiPlatform.pricingStatusMissing')
  })
})
