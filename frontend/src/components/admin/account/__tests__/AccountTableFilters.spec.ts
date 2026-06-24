import { defineComponent } from 'vue'
import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import AccountTableFilters from '../AccountTableFilters.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key
    })
  }
})

const SelectStub = defineComponent({
  name: 'Select',
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

function mountFilters() {
  return mount(AccountTableFilters, {
    props: {
      searchQuery: '',
      filters: {
        platform: '',
        type: '',
        status: '',
        privacy_mode: '',
        group: ''
      },
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
  it('offers a virtual Kiro stub platform filter and emits the sentinel value', async () => {
    const wrapper = mountFilters()

    const kiroStubOption = wrapper.find('[data-value="__kiro_stub__"]')
    expect(kiroStubOption.exists()).toBe(true)
    expect(kiroStubOption.text()).toBe('admin.accounts.kiroStubPlatform')

    await kiroStubOption.trigger('click')

    expect(wrapper.emitted('update:filters')?.[0]?.[0]).toMatchObject({
      platform: '__kiro_stub__'
    })
    expect(wrapper.emitted('change')).toBeTruthy()
  })
})
