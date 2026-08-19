import { describe, expect, it, vi } from 'vitest'
import { createPinia } from 'pinia'
import { mount, shallowMount } from '@vue/test-utils'
import ModelPlazaContent from '../ModelPlazaContent.vue'
import PlazaFilterBar from '../PlazaFilterBar.vue'
import PlazaGroupSection from '../PlazaGroupSection.vue'
import type { ModelPlazaGroup, ModelPlazaResponse } from '@/api/modelPlaza'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

function group(id: number, platform: string): ModelPlazaGroup {
  return {
    id,
    name: `Group ${id}`,
    description: '',
    platform,
    subscription_type: 'standard',
    rate_multiplier: 1,
    peak_rate_enabled: false,
    peak_start: '',
    peak_end: '',
    peak_rate_multiplier: 1,
    is_exclusive: false,
    image_rate_independent: false,
    image_rate_multiplier: 1,
    models: [],
  }
}

describe('model plaza public platform taxonomy', () => {
  it('collapses Gemini and Antigravity into one Google filter without losing either group', async () => {
    const response: ModelPlazaResponse = {
      description: '',
      groups: [group(1, 'gemini'), group(2, 'antigravity')],
    }
    const wrapper = shallowMount(ModelPlazaContent, {
      props: { response, loading: false },
      global: { plugins: [createPinia()] },
    })

    const filter = wrapper.getComponent(PlazaFilterBar)
    expect(filter.props('platforms')).toEqual(['google'])

    filter.vm.$emit('update:platform', 'google')
    await wrapper.vm.$nextTick()
    expect(wrapper.findAllComponents(PlazaGroupSection)).toHaveLength(2)
  })

  it('renders the canonical Google label instead of an internal source key', () => {
    const wrapper = mount(PlazaFilterBar, {
      props: {
        platforms: ['google'],
        groups: [{ id: 1, name: 'Google group', platform: 'google', rate: 1 }],
        rates: [1],
        platform: 'all',
        groupId: 'all',
        rate: 'all',
        search: '',
      },
    })

    expect(wrapper.text()).toContain('Google')
    expect(wrapper.text()).not.toContain('google')
    expect(wrapper.text()).not.toContain('antigravity')
  })
})
