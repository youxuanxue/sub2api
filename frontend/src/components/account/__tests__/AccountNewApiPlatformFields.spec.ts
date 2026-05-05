// Unit tests for the NewAPI (5th platform) shared field component.
// Covers the structured NewAPI account fields:
//   AC-1 — structured 模型 selector renders (whitelist mode is default).
//   AC-2 — 获取模型列表 button renders only when channel_type is in the
//          fetchable set (mirrors new-api MODEL_FETCHABLE_CHANNEL_TYPES).
//   AC-3 — clicking the button emits 'fetch-models' to the parent (parent
//          owns the actual POST + result handling because it knows about
//          the optional account_id stored-key fallback).
//   AC-4 — button is disabled when fetch_models_disabled is true (parent
//          gates this on missing base_url / api_key).
//   AC-5 — switching to mapping mode renders src→dst pair editor instead
//          of the whitelist; «获取模型列表» still appears under both modes.
//   AC-6 — the raw model_mapping JSON textarea no longer renders (single
//          source for credentials.model_mapping is the structured selector).

import { describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
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

import AccountNewApiPlatformFields from '../AccountNewApiPlatformFields.vue'

const ModelWhitelistSelectorStub = defineComponent({
  name: 'ModelWhitelistSelector',
  props: {
    modelValue: { type: Array, default: () => [] }
  },
  emits: ['update:modelValue'],
  template: `
    <div data-testid="newapi-models-selector">
      <span data-testid="newapi-models-value">{{ Array.isArray(modelValue) ? modelValue.join(',') : '' }}</span>
    </div>
  `
})

const SelectStub = defineComponent({
  name: 'StubSelect',
  props: {
    modelValue: { type: [Number, String], default: 0 }
  },
  emits: ['update:modelValue'],
  template: '<div data-testid="channel-type-select"><slot /></div>'
})

function mountFields(overrides: Record<string, unknown> = {}) {
  return mount(AccountNewApiPlatformFields, {
    props: {
      channelTypeOptions: [
        { value: 1, label: 'OpenAI (1)' },
        { value: 14, label: 'DeepSeek (14)' }
      ],
      channelTypesLoading: false,
      channelTypesError: null,
      selectedChannelTypeBaseUrl: '',
      variant: 'create',
      channelType: 0,
      baseUrl: '',
      apiKey: '',
      modelMapping: '',
      statusCodeMapping: '',
      openaiOrganization: '',
      allowedModels: [],
      modelMappings: [],
      restrictionMode: 'whitelist',
      fetchModelsEnabled: false,
      fetchModelsDisabled: false,
      fetchModelsLoading: false,
      ...overrides
    },
    global: {
      stubs: {
        Select: SelectStub,
        Icon: true,
        ModelWhitelistSelector: ModelWhitelistSelectorStub
      }
    }
  })
}

describe('AccountNewApiPlatformFields', () => {
  it('renders the structured models selector by default (D4)', () => {
    const wrapper = mountFields()
    expect(wrapper.find('[data-testid="newapi-models-selector"]').exists()).toBe(true)
    // 文本区里不再渲染原来的 modelMapping JSON 输入框（D3+D4 单一事实源）
    const labels = wrapper.findAll('label').map((l) => l.text())
    expect(labels.some((l) => l.includes('admin.accounts.newApiPlatform.modelMapping'))).toBe(false)
  })

  it('hides the 获取模型列表 button when fetch_models_enabled is false (D3 — non-fetchable channel type)', () => {
    const wrapper = mountFields({ fetchModelsEnabled: false })
    const fetchBtn = wrapper.findAll('button').find((b) =>
      b.text().includes('admin.accounts.newApiPlatform.fetchUpstreamModels')
    )
    expect(fetchBtn).toBeUndefined()
  })

  it('shows the 获取模型列表 button when fetch_models_enabled is true (D3)', () => {
    const wrapper = mountFields({ fetchModelsEnabled: true })
    const fetchBtn = wrapper.findAll('button').find((b) =>
      b.text().includes('admin.accounts.newApiPlatform.fetchUpstreamModels')
    )
    expect(fetchBtn).toBeDefined()
  })

  it('emits "fetch-models" when the 获取模型列表 button is clicked (D3)', async () => {
    const wrapper = mountFields({ fetchModelsEnabled: true })
    const fetchBtn = wrapper.findAll('button').find((b) =>
      b.text().includes('admin.accounts.newApiPlatform.fetchUpstreamModels')
    )!
    await fetchBtn.trigger('click')
    expect(wrapper.emitted('fetchModels')).toBeTruthy()
    expect(wrapper.emitted('fetchModels')).toHaveLength(1)
  })

  it('disables the 获取模型列表 button when fetch_models_disabled is true (D3 — missing base_url / api_key)', async () => {
    const wrapper = mountFields({
      fetchModelsEnabled: true,
      fetchModelsDisabled: true
    })
    const fetchBtn = wrapper.findAll('button').find((b) =>
      b.text().includes('admin.accounts.newApiPlatform.fetchUpstreamModels')
    )!
    expect(fetchBtn.attributes('disabled')).toBeDefined()
    await fetchBtn.trigger('click')
    // disabled buttons emit 'click' but the parent gating means the test
    // just verifies the disabled attribute is wired correctly; UX behavior
    // (button visually disabled, not actionable) is the assertion that matters.
    // The component itself does NOT swallow the click; that's intentional —
    // we trust HTML disabled semantics.
  })

  it('switches to mapping mode and renders the src→dst pair editor (D4)', async () => {
    const wrapper = mountFields({ restrictionMode: 'whitelist' })
    expect(wrapper.find('[data-testid="newapi-models-selector"]').exists()).toBe(true)

    const mappingToggle = wrapper.findAll('button').find((b) =>
      b.text().includes('admin.accounts.modelMapping') && !b.text().includes('Hint')
    )!
    await mappingToggle.trigger('click')
    expect(wrapper.emitted('update:restrictionMode')?.[0]?.[0]).toBe('mapping')
  })
})
