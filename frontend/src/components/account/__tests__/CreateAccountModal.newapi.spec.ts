// Integration tests for CreateAccountModal's NewAPI (5th platform) path.
// Covers D1 + D2 from docs/accounts/newapi-add-account-ui-gap-analysis.md:
//   AC-D1 — fresh open → click NewAPI immediately → accountCategory auto-flips
//           to 'apikey' so form.type becomes 'apikey' (matches the submit-side
//           hard-coded type:'apikey' for newapi). Without this, the model
//           section was hidden because the generic apikey block stayed v-if=false.
//   AC-D2 — switching from OpenAI/API Key → NewAPI no longer renders the
//           generic apikey block (which contained a duplicate base_url +
//           api_key with a misleading anthropic placeholder). Only the
//           NewAPI fields block renders.
//
// We mount the real component but stub heavy children (BaseDialog, Select,
// ProxySelector, GroupSelector, ModelWhitelistSelector) and mock all admin
// API calls so the test focuses on Vue reactivity / v-if behavior.

import { describe, expect, it, vi, beforeEach } from 'vitest'
import { defineComponent, nextTick } from 'vue'
import { mount } from '@vue/test-utils'

const { listChannelTypesMock, fetchUpstreamModelsMock, createAccountMock } = vi.hoisted(() => {
  return {
    listChannelTypesMock: vi.fn(),
    fetchUpstreamModelsMock: vi.fn(),
    createAccountMock: vi.fn()
  }
})

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn(),
    showInfo: vi.fn()
  })
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({
    isSimpleMode: true
  })
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      create: createAccountMock,
      checkMixedChannelRisk: vi.fn().mockResolvedValue({ has_risk: false }),
      exchangeCode: vi.fn()
    },
    settings: {
      getWebSearchEmulationConfig: vi.fn().mockResolvedValue({ enabled: false, providers: [] }),
      getSettings: vi.fn().mockResolvedValue({ account_quota_notify_enabled: false })
    },
    tlsFingerprintProfiles: {
      list: vi.fn().mockResolvedValue([])
    }
  }
}))

vi.mock('@/api/admin/channels', () => ({
  listChannelTypes: listChannelTypesMock,
  fetchUpstreamModels: fetchUpstreamModelsMock
}))

vi.mock('@/api/admin/accounts', () => ({
  getAntigravityDefaultModelMapping: vi.fn().mockResolvedValue([])
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) =>
        params ? `${key}::${JSON.stringify(params)}` : key
    })
  }
})

import CreateAccountModal from '../CreateAccountModal.vue'

const BaseDialogStub = defineComponent({
  name: 'BaseDialog',
  props: { show: { type: Boolean, default: false } },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>'
})

const SelectStub = defineComponent({
  name: 'StubSelect',
  props: { modelValue: { type: [Number, String], default: 0 } },
  emits: ['update:modelValue'],
  template: '<div data-testid="generic-select"><slot /></div>'
})

const ModelWhitelistSelectorStub = defineComponent({
  name: 'ModelWhitelistSelector',
  props: { modelValue: { type: Array, default: () => [] } },
  emits: ['update:modelValue'],
  template: `
    <div data-testid="model-whitelist-selector">
      <span>{{ Array.isArray(modelValue) ? modelValue.join(',') : '' }}</span>
    </div>
  `
})

function mountModal() {
  listChannelTypesMock.mockResolvedValue([
    { channel_type: 1, name: 'OpenAI', api_type: 0, has_adaptor: true, base_url: 'https://api.openai.com' },
    { channel_type: 14, name: 'DeepSeek', api_type: 0, has_adaptor: true, base_url: 'https://api.deepseek.com' }
  ])
  return mount(CreateAccountModal, {
    props: {
      show: true,
      proxies: [],
      groups: []
    },
    global: {
      stubs: {
        BaseDialog: BaseDialogStub,
        Select: SelectStub,
        Icon: true,
        ProxySelector: true,
        GroupSelector: true,
        ModelWhitelistSelector: ModelWhitelistSelectorStub,
        OAuthAuthorizationFlow: true,
        QuotaLimitCard: true,
        ConfirmDialog: true
      }
    }
  })
}

describe('CreateAccountModal — NewAPI (5th platform)', () => {
  beforeEach(() => {
    listChannelTypesMock.mockReset()
    fetchUpstreamModelsMock.mockReset()
    createAccountMock.mockReset()
  })

  // Helper: locate the platform segment buttons by their label text.
  function clickPlatform(wrapper: ReturnType<typeof mountModal>, label: string) {
    const btn = wrapper.findAll('button').find((b) => b.text().trim() === label)
    if (!btn) {
      throw new Error(`could not find platform button "${label}". buttons=${wrapper.findAll('button').map((b) => b.text()).join('|')}`)
    }
    return btn.trigger('click')
  }

  it('AC-D1: clicking NewAPI from fresh-open auto-flips accountCategory to apikey so the model selector renders', async () => {
    const wrapper = mountModal()
    await nextTick()

    // Sanity: model whitelist selector is NOT rendered yet (anthropic + oauth-based).
    expect(wrapper.find('[data-testid="model-whitelist-selector"]').exists()).toBe(false)

    // Switch to NewAPI.
    await clickPlatform(wrapper, 'Extension Engine')
    await nextTick()
    await nextTick()

    // Channel type selector must be present immediately (placed directly under the platform row).
    expect(wrapper.html()).toContain('admin.accounts.newApiPlatform.channelType')

    // The NewAPI fields block must render the structured model selector
    // (D4) — proves D1 succeeded (accountCategory was flipped → form.type='apikey'
    // → the structured selector inside AccountNewApiPlatformFields shows up
    // because the component is mounted and its default mode is 'whitelist').
    const selectors = wrapper.findAll('[data-testid="model-whitelist-selector"]')
    expect(selectors.length).toBeGreaterThanOrEqual(1)
  })

  it('AC-D2: switching OpenAI/API Key → NewAPI does NOT render a duplicate generic apikey block', async () => {
    const wrapper = mountModal()
    await nextTick()

    // Path 2 setup: OpenAI → API Key → NewAPI.
    await clickPlatform(wrapper, 'OpenAI')
    await nextTick()
    // Find and click the OpenAI "API Key" account-type card — there are
    // multiple "API Key" buttons across platforms; we pick the one inside
    // the OpenAI section by class hint (purple key icon block).
    const apiKeyBtns = wrapper.findAll('button').filter((b) => b.text().includes('API Key'))
    if (apiKeyBtns.length > 0) {
      // Pick the first one — for OpenAI section it should be the apikey card.
      await apiKeyBtns[0].trigger('click')
    }
    await nextTick()

    // Now switch to NewAPI.
    await clickPlatform(wrapper, 'Extension Engine')
    await nextTick()
    await nextTick()

    // Count occurrences of the «admin.accounts.baseUrl» label in the DOM.
    // The generic apikey block uses i18n key admin.accounts.baseUrl (label),
    // while the NewAPI fields use admin.accounts.newApiPlatform.baseUrl.
    // After D2, only the NewAPI label should appear; the generic one should not.
    const html = wrapper.html()
    const genericBaseUrlOccurrences = (html.match(/admin\.accounts\.baseUrl(?!Hint)/g) ?? []).length
    expect(genericBaseUrlOccurrences).toBe(0)

    // The NewAPI baseUrl label IS expected.
    const newapiBaseUrlOccurrences = (html.match(/admin\.accounts\.newApiPlatform\.baseUrl(?!Hint)/g) ?? []).length
    expect(newapiBaseUrlOccurrences).toBeGreaterThanOrEqual(1)
  })
})
