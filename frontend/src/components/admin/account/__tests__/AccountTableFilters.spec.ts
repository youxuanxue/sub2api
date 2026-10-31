import { defineComponent } from 'vue'
import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import AccountTableFilters from '../AccountTableFilters.vue'

const loadChannelTypes = vi.fn().mockResolvedValue(undefined)

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key
    })
  }
})

vi.mock('@/composables/useNewApiChannelTypes', () => ({
  useNewApiChannelTypes: () => ({
    types: {
      value: [
        { channel_type: 17, name: 'Ali', api_type: 0, has_adaptor: true, base_url: '' },
        { channel_type: 14, name: 'DeepSeek', api_type: 0, has_adaptor: true, base_url: '' }
      ]
    },
    loading: { value: false },
    error: { value: null },
    load: loadChannelTypes
  })
}))

const SelectStub = defineComponent({
  name: 'SelectStub',
  props: {
    modelValue: { type: [String, Number, Boolean], default: '' },
    options: { type: Array, default: () => [] }
  },
  emits: ['update:modelValue', 'change'],
  template: `
    <div data-testid="select">
      <button
        v-for="opt in options"
        :key="String(opt.value)"
        type="button"
        :data-value="String(opt.value)"
        @click="$emit('update:modelValue', opt.value); $emit('change')"
      >
        {{ opt.label }}
      </button>
    </div>
  `
})

const SearchInputStub = defineComponent({
  name: 'SearchInput',
  props: {
    modelValue: { type: String, default: '' }
  },
  emits: ['update:modelValue', 'search'],
  template: '<input :value="modelValue" @input="$emit(\'update:modelValue\', $event.target.value)" @change="$emit(\'search\')" />'
})

function mountFilters(filters: Record<string, unknown> = {}) {
  return mount(AccountTableFilters, {
    props: {
      searchQuery: '',
      filters: {
        platform: '',
        type: '',
        status: '',
        privacy_mode: '',
        group: '',
        channel_type: '',
        ...filters
      },
      proxies: [{ id: 1, name: 'Office proxy' }, { id: 2, name: 'Backup proxy' }] as any,
      groups: []
    },
    global: {
      stubs: {
        Select: SelectStub,
        SearchInput: SearchInputStub
      }
    }
  })
}

describe('AccountTableFilters', () => {
  it('shows every filter in the row without an extra filters toggle', () => {
    const wrapper = mountFilters()

    expect(wrapper.find('button[aria-controls]').exists()).toBe(false)
    for (const label of [
      'admin.accounts.allPlatforms',
      'admin.accounts.allStatus',
      'admin.accounts.allProxies',
      'admin.accounts.allTypes',
      'admin.accounts.allPrivacyModes',
      'admin.accounts.allGroups'
    ]) {
      expect(wrapper.find(`[aria-label="${label}"]`).isVisible()).toBe(true)
    }
  })

  it('does not offer the retired Kiro stub or group-only composite platform filters', () => {
    const wrapper = mountFilters()

    expect(wrapper.find('[data-value="__kiro_stub__"]').exists()).toBe(false)
    expect(wrapper.find('[data-value="composite"]').exists()).toBe(false)
    expect(wrapper.find('[data-value="kiro"]').exists()).toBe(true)
    expect(wrapper.find('[data-value="newapi"]').exists()).toBe(true)
  })

  it('shows channel type options only for Extension Engine and emits the selected type', async () => {
    const hidden = mountFilters()
    expect(hidden.find('[data-value="17"]').exists()).toBe(false)

    const wrapper = mountFilters({ platform: 'newapi' })
    expect(loadChannelTypes).toHaveBeenCalled()
    expect(wrapper.find('[data-value="17"]').exists()).toBe(true)
    expect(wrapper.find('[data-value="14"]').text()).toBe('DeepSeek')

    await wrapper.find('[data-value="17"]').trigger('click')
    expect(wrapper.emitted('update:filters')?.[0]?.[0]).toMatchObject({
      platform: 'newapi',
      channel_type: '17'
    })
    expect(wrapper.emitted('change')).toBeTruthy()
  })

  it('selects a proxy by its management name and clears the filter', async () => {
    const wrapper = mountFilters({ platform: 'openai', proxy_name: 'Office proxy' })
    await wrapper.find('[data-value="Backup proxy"]').trigger('click')
    expect(wrapper.emitted('update:filters')?.at(-1)?.[0]).toMatchObject({
      platform: 'openai', proxy_name: 'Backup proxy'
    })
    const proxySelect = wrapper.find('[aria-label="admin.accounts.allProxies"]')
    await proxySelect.find('[data-value=""]').trigger('click')
    expect(wrapper.emitted('update:filters')?.at(-1)?.[0]).toMatchObject({
      platform: 'openai', proxy_name: ''
    })
  })

  it('clears channel_type when leaving Extension Engine', async () => {
    const wrapper = mountFilters({ platform: 'newapi', channel_type: '17' })

    await wrapper.find('[data-value="anthropic"]').trigger('click')
    expect(wrapper.emitted('update:filters')?.[0]?.[0]).toMatchObject({
      platform: 'anthropic',
      channel_type: ''
    })
  })
})
