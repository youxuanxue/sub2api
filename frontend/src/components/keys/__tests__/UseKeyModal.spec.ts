import { describe, expect, it, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { nextTick } from 'vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key
  })
}))

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({
    copyToClipboard: vi.fn().mockResolvedValue(true)
  })
}))

// Mocking the catalog API also severs the real api/client → i18n import chain,
// keeping the partial vue-i18n mock above sufficient.
const getMePricingCatalog = vi.fn()
const getPublicPricing = vi.fn()
vi.mock('@/api/me-pricing', () => ({
  getMePricingCatalog: (...args: unknown[]) => getMePricingCatalog(...args)
}))
vi.mock('@/api/pricing', () => ({
  getPublicPricing: (...args: unknown[]) => getPublicPricing(...args)
}))

import UseKeyModal from '../UseKeyModal.vue'
import UseKeyGuide from '../UseKeyGuide.vue'

const STUBS = {
  BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' },
  Icon: { template: '<span />' }
}

function mountModal(props: Record<string, unknown>) {
  return mount(UseKeyModal, {
    props: { show: true, apiKey: 'sk-test', baseUrl: 'https://example.com/v1', ...props },
    global: { stubs: STUBS }
  })
}

function mountQuickstartGuide(props: Record<string, unknown>) {
  return mount(UseKeyGuide, {
    props: {
      apiKey: 'sk-test',
      apiKeyId: 7,
      baseUrl: 'https://example.com',
      platform: null,
      routingMode: 'universal',
      showClientTabs: false,
      ...props,
    },
    global: { stubs: STUBS },
  })
}

beforeEach(() => {
  getMePricingCatalog.mockReset()
  getPublicPricing.mockReset()
  getMePricingCatalog.mockResolvedValue({ models: [] })
  getPublicPricing.mockResolvedValue({ data: [] })
})

describe('UseKeyModal — preserved snippet correctness', () => {
  it('renders GPT-5.5 and goals feature in OpenAI Codex config', () => {
    const wrapper = mountModal({ platform: 'openai' })
    const codeBlocks = wrapper.findAll('pre code').map((code) => code.text())
    const configToml = codeBlocks.find((content) => content.includes('model_provider = "OpenAI"'))

    expect(configToml).toBeDefined()
    expect(configToml).toContain('model = "gpt-5.5"')
    expect(configToml).toContain('review_model = "gpt-5.5"')
    expect(configToml).toContain('[features]\ngoals = true')
    expect(codeBlocks).toContain('{\n  "OPENAI_API_KEY": "sk-test"\n}')
    expect(wrapper.text()).toContain('auth.json')
  })

  it('renders GPT-5.5 in OpenAI Codex WebSocket config', async () => {
    const wrapper = mountModal({ platform: 'openai' })
    const wsTab = wrapper.findAll('button').find((b) => b.text().includes('keys.useKeyModal.cliTabs.codexCliWs'))
    expect(wsTab).toBeDefined()
    await wsTab!.trigger('click')
    await nextTick()

    const codeBlocks = wrapper.findAll('pre code').map((code) => code.text())
    const configToml = codeBlocks.find((content) => content.includes('supports_websockets = true'))
    expect(configToml).toBeDefined()
    expect(configToml).toContain('model = "gpt-5.5"')
    expect(configToml).toContain('requires_openai_auth = true')
    expect(configToml).not.toContain('x-openai-actor-authorization')
    expect(configToml).toContain('[features]\nresponses_websockets_v2 = true\ngoals = true')
    expect(codeBlocks).toContain('{\n  "OPENAI_API_KEY": "sk-test"\n}')
    expect(wrapper.text()).toContain('auth.json')
  })

  it('uses the current API-key auth contract without legacy actor headers', () => {
    const wrapper = mountModal({ platform: 'openai' })
    const codeBlocks = wrapper.findAll('pre code').map((code) => code.text())
    const configToml = codeBlocks.find((content) => content.includes('model_provider = "OpenAI"'))

    expect(configToml).toBeDefined()
    expect(configToml).toContain('requires_openai_auth = true')
    expect(configToml).not.toContain('x-openai-actor-authorization')
    expect(codeBlocks).toContain('{\n  "OPENAI_API_KEY": "sk-test"\n}')
  })

  it('retains the current Codex auth contract when the modal reopens or platform changes', async () => {
    const wrapper = mountModal({ platform: 'openai' })
    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true })
    await nextTick()

    expect(wrapper.findAll('pre code').map((code) => code.text()).join('\n')).toContain('requires_openai_auth = true')

    await wrapper.setProps({ platform: 'gemini' })
    await wrapper.setProps({ platform: 'openai' })
    await nextTick()

    const code = wrapper.findAll('pre code').map((block) => block.text()).join('\n')
    expect(code).toContain('requires_openai_auth = true')
    expect(code).not.toContain('x-openai-actor-authorization')
  })

  it('renders GPT-5.4 mini entry in OpenCode config', async () => {
    const wrapper = mountModal({ platform: 'openai' })
    const opencodeTab = wrapper.findAll('button').find((b) => b.text().includes('keys.useKeyModal.cliTabs.opencode'))
    await opencodeTab!.trigger('click')
    await nextTick()

    const codeBlock = wrapper.find('pre code')
    expect(codeBlock.text()).toContain('"name": "GPT-5.4 Mini"')
  })

  it('renders the current GPT-5.5 and GPT-5.4 reasoning variants in OpenCode config', async () => {
    const wrapper = mount(UseKeyModal, {
      props: {
        show: true,
        apiKey: 'sk-test',
        baseUrl: 'https://example.com/v1',
        platform: 'openai'
      },
      global: {
        stubs: {
          BaseDialog: {
            template: '<div><slot /><slot name="footer" /></div>'
          },
          Icon: {
            template: '<span />'
          }
        }
      }
    })

    const opencodeTab = wrapper.findAll('button').find((button) =>
      button.text().includes('keys.useKeyModal.cliTabs.opencode')
    )
    expect(opencodeTab).toBeDefined()
    await opencodeTab!.trigger('click')
    await nextTick()

    const parsed = JSON.parse(wrapper.find('pre code').text())
    const models = parsed.provider.openai.models
    for (const model of ['gpt-5.5', 'gpt-5.4', 'gpt-5.4-mini']) {
      expect(models[model]).toBeDefined()
      expect(models[model].variants).toHaveProperty('xhigh')
    }
    expect(models['gpt-5.5'].name).toBe('GPT-5.5')
    expect(models['gpt-5.4-mini'].name).toBe('GPT-5.4 Mini')
  })

  it('renders Claude Fable 5 OpenCode config with adaptive thinking', async () => {
    const wrapper = mountModal({ platform: 'antigravity' })
    const opencodeTab = wrapper.findAll('button').find((b) => b.text().includes('keys.useKeyModal.cliTabs.opencode'))
    await opencodeTab!.trigger('click')
    await nextTick()

    const claudeConfig = wrapper.findAll('pre code').map((c) => c.text()).find((c) => c.includes('"antigravity-claude"'))
    expect(claudeConfig).toBeDefined()
    const fable = JSON.parse(claudeConfig!).provider['antigravity-claude'].models['claude-fable-5']
    expect(fable.name).toBe('Claude Fable 5')
    expect(fable.options.thinking).toEqual({ type: 'adaptive' })
  })

  it('renders empirical gemini wire ids in antigravity Gemini OpenCode config', async () => {
    const wrapper = mountModal({ platform: 'antigravity' })
    const opencodeTab = wrapper.findAll('button').find((b) => b.text().includes('keys.useKeyModal.cliTabs.opencode'))
    await opencodeTab!.trigger('click')
    await nextTick()

    const geminiConfig = wrapper.findAll('pre code').map((c) => c.text()).find((c) => c.includes('"antigravity-gemini"'))
    const models = JSON.parse(geminiConfig!).provider['antigravity-gemini'].models
    expect(models['gemini-3.5-flash-low'].name).toBe('Gemini 3.5 Flash (Medium)')
    expect(models['gemini-pro-agent']).toBeDefined()
    expect(models['gpt-oss-120b-medium']).toBeUndefined()
  })

  it('renders anti-down-grading env vars in Claude Code tab, NONESSENTIAL_TRAFFIC commented out', async () => {
    const wrapper = mountModal({ platform: 'openai', allowMessagesDispatch: true })
    const claudeTab = wrapper.findAll('button').find((b) => b.text().includes('keys.useKeyModal.cliTabs.claudeCode'))
    await claudeTab!.trigger('click')
    await nextTick()

    const joined = wrapper.findAll('pre code').map((c) => c.text()).join('\n')
    expect(joined).toContain('CLAUDE_CODE_DISABLE_ADAPTIVE_THINKING')
    expect(joined).toContain('31999')
    expect(joined).toContain('claude-opus-4-8[1m]')
    const activeBlocks = wrapper.findAll('pre code').map((c) => c.text())
      .filter((s) => /^\s*export CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1\s*$/m.test(s))
    expect(activeBlocks).toHaveLength(0)
  })

  it('hides Claude flavor for an antigravity group without claude scope', async () => {
    const wrapper = mountModal({ platform: 'antigravity', supportedModelScopes: ['gemini_text', 'gemini_image'] })
    await nextTick()
    expect(wrapper.findAll('button').find((b) => b.text().includes('keys.useKeyModal.cliTabs.claudeCode'))).toBeUndefined()
    const opencodeTab = wrapper.findAll('button').find((b) => b.text().includes('keys.useKeyModal.cliTabs.opencode'))
    await opencodeTab!.trigger('click')
    await nextTick()
    const blocks = wrapper.findAll('pre code').map((c) => c.text())
    expect(blocks.some((c) => c.includes('"antigravity-claude"'))).toBe(false)
    expect(blocks.some((c) => c.includes('"antigravity-gemini"'))).toBe(true)
  })
})

describe('UseKeyGuide — tool-first Quickstart contracts', () => {
  it('generates current Qwen Code settings with the key isolated in .env', async () => {
    const wrapper = mountQuickstartGuide({ selectedClient: 'qwen-code', selectedProtocol: 'anthropic' })
    await flushPromises()
    const files = wrapper.findAll('pre code').map((code) => code.text())
    expect(files[0]).toBe('TOKENKEY_API_KEY=sk-test')
    const settings = JSON.parse(files[1])
    expect(settings.security.auth.selectedType).toBe('anthropic')
    expect(settings.modelProviders.anthropic[0]).toMatchObject({
      envKey: 'TOKENKEY_API_KEY',
      baseUrl: 'https://example.com/v1',
    })
    expect(files[1]).not.toContain('sk-test')
    expect(wrapper.find('[data-tk="quickstart-environment-picker"]').exists()).toBe(true)
  })

  it('switches Qwen Code to OpenAI Chat Completions provider without claiming Responses', async () => {
    const wrapper = mountQuickstartGuide({ selectedClient: 'qwen-code', selectedProtocol: 'openai' })
    await flushPromises()
    const settings = JSON.parse(wrapper.findAll('pre code')[1].text())
    expect(settings.security.auth.selectedType).toBe('openai')
    expect(settings.modelProviders.openai[0].baseUrl).toBe('https://example.com/v1')
    expect(wrapper.text()).not.toContain('Responses')
  })

  it('routes Qwen Code Anthropic requests through the Antigravity prefix for a direct key', async () => {
    const wrapper = mountQuickstartGuide({
      selectedClient: 'qwen-code',
      selectedProtocol: 'anthropic',
      platform: 'antigravity',
      routingMode: 'direct',
    })
    await flushPromises()
    const settings = JSON.parse(wrapper.findAll('pre code')[1].text())
    expect(settings.modelProviders.anthropic[0].baseUrl).toBe('https://example.com/antigravity/v1')
  })

  it('renders exact Cline OpenAI Compatible fields without an OS selector or client tabs', async () => {
    const wrapper = mountQuickstartGuide({ selectedClient: 'cline' })
    await flushPromises()
    const fields = wrapper.find('pre code').text()
    expect(fields).toContain('API Provider: OpenAI Compatible')
    expect(fields).toContain('Base URL: https://example.com/v1')
    expect(fields).toContain('Model ID: gpt-5.5')
    expect(wrapper.find('[data-tk="quickstart-environment-picker"]').exists()).toBe(false)
    expect(wrapper.find('[data-tk="quickstart-config-toggle-0"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('keys.useKeyModal.cliTabs.codexCli')
  })

  it('limits OpenCode models to the selected direct key live menu', async () => {
    getMePricingCatalog.mockResolvedValue({
      models: [
        { model_id: 'gpt-5.4-mini', capabilities: [], context_window: 400000, max_output_tokens: 128000 },
        { model_id: 'tenant-custom-model', capabilities: [] },
      ],
    })
    const wrapper = mountQuickstartGuide({
      selectedClient: 'opencode',
      platform: 'openai',
      routingMode: 'direct',
    })
    await flushPromises()

    const config = JSON.parse(wrapper.find('pre code').text())
    expect(Object.keys(config.provider.openai.models)).toEqual([
      'gpt-5.4-mini',
      'tenant-custom-model',
    ])
    expect(config.provider.openai.models['gpt-5.4-mini'].name).toBe('GPT-5.4 Mini')
    expect(config.provider.openai.models['tenant-custom-model']).toEqual({ name: 'tenant-custom-model' })
    expect(config.provider.openai.models['gpt-5.5']).toBeUndefined()
  })

  it.each([
    { platform: 'anthropic', alias: 'sonnet-prod' },
    { platform: 'gemini', alias: 'flash-prod' },
  ])('keeps a direct $platform custom alias in OpenCode models', async ({ platform, alias }) => {
    getMePricingCatalog.mockResolvedValue({
      models: [{ model_id: alias, capabilities: [] }],
    })
    const wrapper = mountQuickstartGuide({
      selectedClient: 'opencode',
      platform,
      routingMode: 'direct',
    })
    await flushPromises()

    const config = JSON.parse(wrapper.find('pre code').text())
    expect(config.provider[platform].models).toEqual({
      [alias]: { name: alias },
    })
  })

  it('uses client-specific copy for cURL instead of Codex setup instructions', async () => {
    const wrapper = mountQuickstartGuide({ selectedClient: 'curl' })
    await flushPromises()
    expect(wrapper.text()).toContain('quickstart.clientConfigNote')
    expect(wrapper.text()).not.toContain('keys.useKeyModal.openai.note')
    expect(wrapper.text()).not.toContain('keys.useKeyModal.openai.description')
  })

  it('renders Dify Tool Call config and TokenKey ceilings without claiming streaming validation', async () => {
    const wrapper = mountQuickstartGuide({
      selectedClient: 'dify',
      keyQuota: 100,
      rateLimit5h: 25,
      rateLimit1d: 0,
      rateLimit7d: 80,
    })
    await flushPromises()
    const files = wrapper.findAll('pre code').map((code) => code.text())
    expect(files[0]).toContain('Function Call Type: Tool Call')
    expect(files[0]).not.toContain('Stream function calling')
    expect(files[1]).toContain('quickstart.keyQuota: $100')
    expect(files[1]).toContain('quickstart.limit1d: quickstart.unlimited')
    expect(wrapper.text()).toContain('quickstart.testToolCall')
    expect(wrapper.text()).toContain('quickstart.difyLimitHint')
  })
})

describe('UseKeyModal — redesign (picker / test / CC-only / raw tabs)', () => {
  it('injects the live servable model into the Gemini CLI snippet (no free-text hint)', async () => {
    getMePricingCatalog.mockResolvedValue({
      models: [
        { model_id: 'gemini-2.5-pro', capabilities: ['vision'], context_window: 1048576 },
        { model_id: 'gemini-2.5-flash', capabilities: [] }
      ]
    })
    const wrapper = mountModal({ platform: 'gemini', apiKeyId: 42 })
    await flushPromises()
    await nextTick()

    expect(getMePricingCatalog).toHaveBeenCalledWith({ apiKeyId: 42 })
    const joined = wrapper.findAll('pre code').map((c) => c.text()).join('\n')
    // first servable gemini model is injected; the old free-text comment is gone
    expect(joined).toContain('GEMINI_MODEL="gemini-2.5-pro"')
    expect(joined).not.toContain('gemini-3-pro-preview')
    // the picker offers exactly the servable ids
    const options = wrapper.findAll('option').map((o) => o.text())
    expect(options).toContain('gemini-2.5-pro')
    expect(options).toContain('gemini-2.5-flash')
  })

  it('CC-only anthropic group offers only Claude Code (no curl/python/opencode) + warning', async () => {
    const wrapper = mountModal({ platform: 'anthropic', apiKeyId: 7, claudeCodeOnly: true })
    await flushPromises()
    await nextTick()

    const tabLabels = wrapper.findAll('button').map((b) => b.text())
    expect(tabLabels.some((t) => t.includes('keys.useKeyModal.cliTabs.claudeCode'))).toBe(true)
    expect(tabLabels.some((t) => t.includes('keys.useKeyModal.cliTabs.curl'))).toBe(false)
    expect(tabLabels.some((t) => t.includes('keys.useKeyModal.cliTabs.python'))).toBe(false)
    expect(tabLabels.some((t) => t.includes('keys.useKeyModal.cliTabs.opencode'))).toBe(false)
    expect(wrapper.text()).toContain('keys.useKeyModal.ccOnlyWarning')
  })

  it('adds cURL + Python tabs for a non-CC anthropic group with a correct curl body', async () => {
    const wrapper = mountModal({ platform: 'anthropic', apiKeyId: 9, claudeCodeOnly: false })
    await flushPromises()
    await nextTick()

    const curlTab = wrapper.findAll('button').find((b) => b.text().includes('keys.useKeyModal.cliTabs.curl'))
    expect(curlTab).toBeDefined()
    await curlTab!.trigger('click')
    await nextTick()

    const snippet = wrapper.findAll('pre code').map((c) => c.text()).join('\n')
    expect(snippet).toContain('/v1/messages')
    expect(snippet).toContain('x-api-key: sk-test')
    expect(snippet).toContain('anthropic-version: 2023-06-01')
  })

  it('emits a correct OpenAI-compat curl (Bearer auth, /v1/chat/completions)', async () => {
    const wrapper = mountModal({ platform: 'openai', apiKeyId: 11 })
    await flushPromises()
    await nextTick()
    const curlTab = wrapper.findAll('button').find((b) => b.text().includes('keys.useKeyModal.cliTabs.curl'))
    await curlTab!.trigger('click')
    await nextTick()
    const snippet = wrapper.findAll('pre code').map((c) => c.text()).join('\n')
    expect(snippet).toContain('/v1/chat/completions')
    expect(snippet).toContain('Authorization: Bearer sk-test')
    expect(snippet).toContain('"max_tokens": 64')
  })

  it('Test key button verifies the key live and shows the verbatim error', async () => {
    getMePricingCatalog.mockResolvedValue({ models: [{ model_id: 'gpt-5.5', capabilities: [] }] })
    const fetchMock = vi.fn().mockResolvedValue({
      ok: false,
      status: 401,
      text: () => Promise.resolve('{"error":{"message":"Invalid API key"}}')
    })
    vi.stubGlobal('fetch', fetchMock)

    const wrapper = mountModal({ platform: 'openai', apiKeyId: 3 })
    await flushPromises()
    await nextTick()

    const testBtn = wrapper.findAll('button').find((b) => b.text().includes('keys.useKeyModal.testKey'))
    expect(testBtn).toBeDefined()
    await testBtn!.trigger('click')
    await flushPromises()
    await nextTick()

    expect(fetchMock).toHaveBeenCalled()
    const calledUrl = fetchMock.mock.calls[0][0] as string
    expect(calledUrl).toContain('/v1/chat/completions')
    expect(wrapper.text()).toContain('Invalid API key')

    vi.unstubAllGlobals()
  })
})

describe('UseKeyModal — universal keys', () => {
  it('shows client guide without requiring a fixed group platform', async () => {
    const wrapper = mountModal({
      platform: null,
      routingMode: 'universal',
      apiKeyId: 42,
    })
    await flushPromises()

    expect(wrapper.text()).not.toContain('keys.useKeyModal.noGroupTitle')
    expect(wrapper.text()).toContain('keys.useKeyModal.cliTabs.claudeCode')
    expect(wrapper.text()).toContain('keys.useKeyModal.cliTabs.codexCli')
  })

  it('loads cross-group model menu via entitlement index instead of per-key catalog', async () => {
    getMePricingCatalog.mockResolvedValue({
      authorized_groups_by_model: {
        'claude-opus-4-8': [{ id: 1, name: 'anthropic' }],
      },
      models: [],
    })
    getPublicPricing.mockResolvedValue({
      data: [
        {
          model_id: 'claude-opus-4-8',
          capabilities: ['thinking'],
          context_window: 200000,
        },
      ],
    })

    const wrapper = mountModal({
      platform: null,
      routingMode: 'universal',
      apiKeyId: 42,
    })
    await flushPromises()

    expect(getMePricingCatalog).toHaveBeenCalledWith()
    expect(getMePricingCatalog).not.toHaveBeenCalledWith(expect.objectContaining({ apiKeyId: 42 }))
    expect(getPublicPricing).toHaveBeenCalled()
    expect(wrapper.text()).not.toContain('keys.useKeyModal.modelsEmpty')
    expect(wrapper.find('select').findAll('option').some((o) => o.text().includes('claude-opus-4-8'))).toBe(true)
  })

  it('rejects a deep-linked model outside the live catalog', async () => {
    getMePricingCatalog.mockResolvedValue({ authorized_groups_by_model: {}, models: [] })
    getPublicPricing.mockResolvedValue({ data: [] })

    const wrapper = mountModal({
      platform: null,
      routingMode: 'universal',
      apiKeyId: 42,
      initialModel: 'claude-haiku-4-5',
    })
    await flushPromises()

    expect(wrapper.text()).toContain('keys.useKeyModal.modelsEmpty')
    expect(wrapper.find('[data-tk="use-key-model-select"]').exists()).toBe(true)
  })
})
