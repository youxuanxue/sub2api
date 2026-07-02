// Integration tests for CreateAccountModal's NewAPI (5th platform) path.
// Covers the NewAPI account category and duplicate generic API-key field regressions:
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
import { flushPromises, mount } from '@vue/test-utils'

const { listChannelTypesMock, fetchUpstreamModelsMock, listChannelTypeModelsMock, createAccountMock } = vi.hoisted(() => {
  return {
    listChannelTypesMock: vi.fn(),
    fetchUpstreamModelsMock: vi.fn(),
    listChannelTypeModelsMock: vi.fn(),
    createAccountMock: vi.fn()
  }
})

const VERTEX_CH41_SERVABLE_MODELS = [
  'gemini-2.5-flash',
  'gemini-2.5-flash-lite',
  'gemini-2.5-pro',
  'imagen-4.0-fast-generate-001',
  'imagen-4.0-generate-001',
  'imagen-4.0-ultra-generate-001',
  'veo-3.1-generate-001',
]

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
  fetchUpstreamModels: fetchUpstreamModelsMock,
  listChannelTypeModels: listChannelTypeModelsMock,
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
import AccountNewApiPlatformFields from '../AccountNewApiPlatformFields.vue'
import { NEW_API_CHANNEL_TYPE_VERTEX_AI } from '@/constants/newApiChannelTypes.tk'

const SAMPLE_VERTEX_SA_JSON = JSON.stringify({
  type: 'service_account',
  project_id: 'tk-vertex-trial',
  private_key_id: 'kid',
  private_key: '-----BEGIN PRIVATE KEY-----\nabc\n-----END PRIVATE KEY-----\n',
  client_email: 'svc@tk-vertex-trial.iam.gserviceaccount.com'
})

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

function mountModalWithRealNewApiChildren() {
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
        ProxySelector: true,
        GroupSelector: true,
        OAuthAuthorizationFlow: true,
        QuotaLimitCard: true,
        ConfirmDialog: true
      }
    }
  })
}

function clickPlatformByLabel(wrapper: ReturnType<typeof mountModal>, label: string) {
  const btn = wrapper.findAll('button').find((b) => b.text().trim() === label)
  if (!btn) {
    throw new Error(`could not find platform button "${label}". buttons=${wrapper.findAll('button').map((b) => b.text()).join('|')}`)
  }
  return btn.trigger('click')
}

describe('CreateAccountModal — NewAPI (5th platform)', () => {
  beforeEach(() => {
    listChannelTypesMock.mockReset()
    fetchUpstreamModelsMock.mockReset()
    listChannelTypeModelsMock.mockReset()
    listChannelTypeModelsMock.mockResolvedValue({})
    createAccountMock.mockReset()
  })

  it('disables native form validation so Vue submit validation always shows a toast', () => {
    const wrapper = mountModal()

    expect(wrapper.find('form#create-account-form').attributes('novalidate')).toBeDefined()
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

    // Channel type / Base URL / API Key must be present immediately after the platform row,
    // before other platform/account blocks and before quota controls.
    const html = wrapper.html()
    const platformIdx = html.indexOf('Extension Engine')
    const channelTypeIdx = html.indexOf('admin.accounts.newApiPlatform.channelType', platformIdx)
    const baseUrlIdx = html.indexOf('admin.accounts.newApiPlatform.baseUrl', platformIdx)
    const apiKeyIdx = html.indexOf('admin.accounts.newApiPlatform.apiKey', platformIdx)
    const accountTypeIdx = html.indexOf('admin.accounts.accountType', platformIdx)
    const quotaControlIdx = html.indexOf('admin.accounts.quotaControl.title', platformIdx)
    expect(platformIdx).toBeGreaterThanOrEqual(0)
    expect(channelTypeIdx).toBeGreaterThan(platformIdx)
    expect(baseUrlIdx).toBeGreaterThan(channelTypeIdx)
    expect(apiKeyIdx).toBeGreaterThan(baseUrlIdx)
    expect(accountTypeIdx === -1 || channelTypeIdx < accountTypeIdx).toBe(true)
    expect(quotaControlIdx === -1 || apiKeyIdx < quotaControlIdx).toBe(true)

    // The NewAPI fields block must render the structured model selector
    // (D4) — proves D1 succeeded (accountCategory was flipped → form.type='apikey'
    // → the structured selector inside AccountNewApiPlatformFields shows up
    // because the component is mounted and its default mode is 'whitelist').
    const selectors = wrapper.findAll('[data-testid="model-whitelist-selector"]')
    expect(selectors.length).toBeGreaterThanOrEqual(1)
  })

  it('renders NewAPI credential fields with the real shared field subtree', async () => {
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    const wrapper = mountModalWithRealNewApiChildren()
    await nextTick()

    await clickPlatform(wrapper, 'Extension Engine')
    await nextTick()
    await nextTick()

    const html = wrapper.html()
    const platformIdx = html.indexOf('Extension Engine')
    const channelTypeIdx = html.indexOf('admin.accounts.newApiPlatform.channelType', platformIdx)
    const baseUrlIdx = html.indexOf('admin.accounts.newApiPlatform.baseUrl', platformIdx)
    const apiKeyIdx = html.indexOf('admin.accounts.newApiPlatform.apiKey', platformIdx)
    const quotaControlIdx = html.indexOf('admin.accounts.quotaControl.title', platformIdx)

    expect(channelTypeIdx).toBeGreaterThan(platformIdx)
    expect(baseUrlIdx).toBeGreaterThan(channelTypeIdx)
    expect(apiKeyIdx).toBeGreaterThan(baseUrlIdx)
    expect(quotaControlIdx === -1 || apiKeyIdx < quotaControlIdx).toBe(true)
    expect(wrapper.find('input[type="password"]').exists()).toBe(true)
    expect(errorSpy).not.toHaveBeenCalled()

    errorSpy.mockRestore()
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

function mountModalWithVertexNewApiCatalog() {
  listChannelTypesMock.mockResolvedValue([
    { channel_type: 14, name: 'DeepSeek', api_type: 0, has_adaptor: true, base_url: 'https://api.deepseek.com' },
    {
      channel_type: NEW_API_CHANNEL_TYPE_VERTEX_AI,
      name: 'Vertex AI',
      api_type: 0,
      has_adaptor: true,
      base_url: ''
    }
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

async function selectNewapiVertexChannel(wrapper: ReturnType<typeof mountModal>) {
  await clickPlatformByLabel(wrapper, 'Extension Engine')
  await flushPromises()
  await nextTick()

  const newApiFields = wrapper.findComponent(AccountNewApiPlatformFields)
  expect(newApiFields.exists()).toBe(true)
  await newApiFields.setValue(NEW_API_CHANNEL_TYPE_VERTEX_AI, 'channelType')
  await nextTick()
  await flushPromises()
  return newApiFields
}

describe('CreateAccountModal — NewAPI Vertex (channel_type 41)', () => {
  beforeEach(() => {
    listChannelTypesMock.mockReset()
    fetchUpstreamModelsMock.mockReset()
    listChannelTypeModelsMock.mockReset()
    createAccountMock.mockReset()
    createAccountMock.mockResolvedValue({ id: 880 })
    listChannelTypeModelsMock.mockResolvedValue({ '41': VERTEX_CH41_SERVABLE_MODELS })
  })

  it('AC-V1: hides transport credentials and creates service_account with SA JSON + model_mapping', async () => {
    const wrapper = mountModalWithVertexNewApiCatalog()
    await nextTick()

    await selectNewapiVertexChannel(wrapper)
    await flushPromises()
    await nextTick()

    expect(listChannelTypeModelsMock).toHaveBeenCalled()
    expect(wrapper.find('[data-testid="model-whitelist-selector"]').text()).toContain('gemini-2.5-flash')

    expect(wrapper.html()).not.toMatch(/admin\.accounts\.newApiPlatform\.apiKey(?!Hint)/)
    expect(wrapper.find('[data-testid="vertex-sa-json-input"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('admin.accounts.vertexNewapiMediaHint')

    await wrapper.find('input[data-tour="account-form-name"]').setValue('vertex-trial-01')
    const jsonInput = wrapper.find('[data-testid="vertex-sa-json-input"]')
    await jsonInput.setValue(SAMPLE_VERTEX_SA_JSON)
    await jsonInput.trigger('change')
    await nextTick()

    await wrapper.find('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    expect(createAccountMock).toHaveBeenCalledWith(
      expect.objectContaining({
        name: 'vertex-trial-01',
        platform: 'newapi',
        type: 'service_account',
        channel_type: NEW_API_CHANNEL_TYPE_VERTEX_AI,
        credentials: expect.objectContaining({
          service_account_json: expect.stringContaining('tk-vertex-trial'),
          project_id: 'tk-vertex-trial',
          client_email: 'svc@tk-vertex-trial.iam.gserviceaccount.com',
          location: 'us-central1',
          tier_id: 'vertex',
          model_mapping: Object.fromEntries(
            VERTEX_CH41_SERVABLE_MODELS.map((id) => [id, id] as const)
          ),
        })
      })
    )
    const payload = createAccountMock.mock.calls[0][0] as { credentials: Record<string, unknown> }
    expect(payload.credentials).not.toHaveProperty('api_key')
    expect(payload.credentials).not.toHaveProperty('base_url')
  })

  it('AC-V2: rejects submit when model_mapping is empty for channel_type 41', async () => {
    const wrapper = mountModalWithVertexNewApiCatalog()
    await nextTick()

    const newApiFields = await selectNewapiVertexChannel(wrapper)
    await flushPromises()

    newApiFields.vm.$emit('update:allowedModels', [])
    await nextTick()

    await wrapper.find('input[data-tour="account-form-name"]').setValue('vertex-no-mapping')
    const jsonInput = wrapper.find('[data-testid="vertex-sa-json-input"]')
    await jsonInput.setValue(SAMPLE_VERTEX_SA_JSON)
    await jsonInput.trigger('change')
    await nextTick()

    await wrapper.find('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(createAccountMock).not.toHaveBeenCalled()
  })

  it('AC-V3: rejects submit when Service Account JSON is missing for channel_type 41', async () => {
    const wrapper = mountModalWithVertexNewApiCatalog()
    await nextTick()

    const newApiFields = await selectNewapiVertexChannel(wrapper)
    newApiFields.vm.$emit('update:allowedModels', ['imagen-4.0-fast-generate-001'])
    await nextTick()

    await wrapper.find('input[data-tour="account-form-name"]').setValue('vertex-no-json')

    await wrapper.find('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(createAccountMock).not.toHaveBeenCalled()
  })
})

describe('CreateAccountModal — Grok relay stub', () => {
  beforeEach(() => {
    listChannelTypesMock.mockReset()
    fetchUpstreamModelsMock.mockReset()
    listChannelTypeModelsMock.mockReset()
    listChannelTypeModelsMock.mockResolvedValue({})
    createAccountMock.mockReset()
    createAccountMock.mockResolvedValue({ id: 953 })
  })

  it('creates Grok relay stubs as first-class grok apikey accounts', async () => {
    const wrapper = mountModal()
    await nextTick()

    await clickPlatformByLabel(wrapper, 'Grok')
    await nextTick()
    await nextTick()

    const relayButton = wrapper.findAll('button').find((b) =>
      b.text().includes('admin.accounts.grokPlatform.relayMode')
    )
    expect(relayButton).toBeTruthy()
    await relayButton!.trigger('click')
    await nextTick()
    await nextTick()

    await wrapper.find('input[data-tour="account-form-name"]').setValue('grok-us4')
    const baseUrlInput = wrapper.find('input[placeholder="https://api-us4.tokenkey.dev"]')
    const apiKeyInput = wrapper.find('input[placeholder="tk-edge-..."]')
    expect(baseUrlInput.exists()).toBe(true)
    expect(apiKeyInput.exists()).toBe(true)
    await baseUrlInput.setValue('https://api-us4.tokenkey.dev')
    await apiKeyInput.setValue('edge-tokenkey-key')

    await wrapper.find('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    expect(createAccountMock).toHaveBeenCalledWith(expect.objectContaining({
      name: 'grok-us4',
      platform: 'grok',
      type: 'apikey',
      credentials: {
        base_url: 'https://api-us4.tokenkey.dev',
        api_key: 'edge-tokenkey-key',
        mirror_platform: 'grok'
      }
    }))
  })

  it('does not create a Grok relay stub without an edge base URL', async () => {
    const wrapper = mountModal()
    await nextTick()

    await clickPlatformByLabel(wrapper, 'Grok')
    await nextTick()
    await nextTick()

    const relayButton = wrapper.findAll('button').find((b) =>
      b.text().includes('admin.accounts.grokPlatform.relayMode')
    )
    expect(relayButton).toBeTruthy()
    await relayButton!.trigger('click')
    await nextTick()
    await nextTick()

    await wrapper.find('input[data-tour="account-form-name"]').setValue('grok-us4')
    await wrapper.find('input[placeholder="tk-edge-..."]').setValue('edge-tokenkey-key')

    await wrapper.find('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(createAccountMock).not.toHaveBeenCalled()
  })
})
