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
vi.mock('@/api/me-pricing', () => ({
  getMePricingCatalog: (...args: unknown[]) => getMePricingCatalog(...args)
}))

import UseKeyModal from '../UseKeyModal.vue'

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

beforeEach(() => {
  getMePricingCatalog.mockReset()
  getMePricingCatalog.mockResolvedValue({ models: [] })
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
  })

  it('renders GPT-5.5 in OpenAI Codex WebSocket config', async () => {
    const wrapper = mountModal({ platform: 'openai' })
    const wsTab = wrapper.findAll('button').find((b) => b.text().includes('keys.useKeyModal.cliTabs.codexCliWs'))
    expect(wsTab).toBeDefined()
    await wsTab!.trigger('click')
    await nextTick()

    const configToml = wrapper.findAll('pre code').map((c) => c.text()).find((c) => c.includes('supports_websockets = true'))
    expect(configToml).toBeDefined()
    expect(configToml).toContain('model = "gpt-5.5"')
    expect(configToml).toContain('[features]\nresponses_websockets_v2 = true\ngoals = true')
  })

  it('renders GPT-5.4 mini entry in OpenCode config', async () => {
    const wrapper = mountModal({ platform: 'openai' })
    const opencodeTab = wrapper.findAll('button').find((b) => b.text().includes('keys.useKeyModal.cliTabs.opencode'))
    await opencodeTab!.trigger('click')
    await nextTick()

    const codeBlock = wrapper.find('pre code')
    expect(codeBlock.text()).toContain('"name": "GPT-5.4 Mini"')
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

  it('hides Claude flavor for a gemini-only antigravity group (scope gate)', async () => {
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
