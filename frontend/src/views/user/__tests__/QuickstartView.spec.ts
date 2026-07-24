import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { nextTick, ref } from 'vue'
import type { ApiKey } from '@/types'

const { listKeys, replaceMock } = vi.hoisted(() => ({
  listKeys: vi.fn(),
  replaceMock: vi.fn(),
}))

const routeQuery = ref<Record<string, string>>({})

vi.mock('vue-router', () => ({
  useRoute: () => ({
    get query() {
      return routeQuery.value
    },
  }),
  useRouter: () => ({ replace: replaceMock }),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
    }),
  }
})

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    cachedPublicSettings: { api_base_url: 'https://api.example.com' },
  }),
}))

vi.mock('@/api/keys', () => ({
  list: (...args: unknown[]) => listKeys(...args),
  create: vi.fn(),
}))

vi.mock('@/components/keys/UseKeyGuide.vue', () => ({
  default: {
    name: 'UseKeyGuide',
    props: [
      'apiKey',
      'apiKeyId',
      'platform',
      'routingMode',
      'initialModel',
      'claudeCodeOnly',
      'allowMessagesDispatch',
      'supportedModelScopes',
      'selectedClient',
      'selectedProtocol',
      'selectedTransport',
      'showClientTabs',
    ],
    template:
      '<div data-test="use-key-guide">{{ routingMode }}|{{ platform ?? "" }}|{{ initialModel ?? "" }}|{{ selectedClient ?? "" }}|{{ selectedProtocol ?? "" }}|{{ selectedTransport ?? "" }}</div>',
  },
}))

import QuickstartView from '../QuickstartView.vue'

const universalKey = (): ApiKey => ({
  id: 42,
  user_id: 1,
  key: 'sk-universal-test-key',
  name: 'Test',
  group_id: null,
  routing_mode: 'universal',
  status: 'active',
  ip_whitelist: [],
  ip_blacklist: [],
  last_used_at: null,
  quota: 0,
  quota_used: 0,
  expires_at: null,
  created_at: '2026-07-06T00:00:00Z',
  updated_at: '2026-07-06T00:00:00Z',
  rate_limit_5h: 0,
  rate_limit_1d: 0,
  rate_limit_7d: 0,
  usage_5h: 0,
  usage_1d: 0,
  usage_7d: 0,
  window_5h_start: null,
  window_1d_start: null,
  window_7d_start: null,
  reset_5h_at: null,
  reset_1d_at: null,
  reset_7d_at: null,
})

const mountView = async () => {
  const wrapper = mount(QuickstartView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        LoadingSpinner: true,
        GroupBadge: true,
        RouterLink: { template: '<a><slot /></a>' },
      },
    },
  })
  await flushPromises()
  await nextTick()
  return wrapper
}

describe('QuickstartView', () => {
  beforeEach(() => {
    routeQuery.value = {}
    listKeys.mockReset()
    replaceMock.mockReset()
    listKeys.mockResolvedValue({
      items: [universalKey()],
      total: 1,
      page: 1,
      page_size: 100,
      pages: 1,
    })
  })

  it('embeds UseKeyGuide for universal keys without a fixed group platform', async () => {
    const wrapper = await mountView()
    const guide = wrapper.get('[data-test="use-key-guide"]')
    expect(guide.text()).toBe('universal|||claude|anthropic|http')
    expect(wrapper.text()).not.toContain('keys.useKeyModal.noGroupTitle')
  })

  it('prefers universal key and passes model query to UseKeyGuide', async () => {
    routeQuery.value = { model: 'claude-haiku-4-5' }
    listKeys.mockResolvedValue({
      items: [
        { ...universalKey(), id: 5, name: 'Direct', routing_mode: 'direct', group_id: 1, group: { id: 1, name: 'claude' } },
        universalKey(),
      ],
      total: 2,
      page: 1,
      page_size: 100,
      pages: 1,
    })
    const wrapper = await mountView()
    expect((wrapper.get('select').element as HTMLSelectElement).value).toBe('42')
    expect(wrapper.get('[data-test="use-key-guide"]').text()).toContain('claude-haiku-4-5')
  })

  it('selects key from ?keyId= query on load', async () => {
    routeQuery.value = { keyId: '42' }
    const wrapper = await mountView()
    expect((wrapper.get('select').element as HTMLSelectElement).value).toBe('42')
  })

  it('hides reserved __tk_probe_* keys from the picker', async () => {
    listKeys.mockResolvedValue({
      items: [
        { ...universalKey(), id: 7, name: '__tk_probe_openai_key' },
        universalKey(),
      ],
      total: 2,
      page: 1,
      page_size: 100,
      pages: 1,
    })
    const wrapper = await mountView()
    const options = wrapper.get('select').findAll('option')
    expect(options).toHaveLength(1)
    expect(options[0].text()).toContain('Test')
    expect(listKeys).toHaveBeenCalledWith(1, 100, { status: 'active' })
  })

  it('syncs selected key to router query when user changes selection', async () => {
    listKeys.mockResolvedValue({
      items: [universalKey(), { ...universalKey(), id: 99, name: 'Other' }],
      total: 2,
      page: 1,
      page_size: 100,
      pages: 2,
    })
    const wrapper = await mountView()
    await wrapper.get('select').setValue('99')
    await nextTick()
    expect(replaceMock).toHaveBeenCalledWith(
      expect.objectContaining({ query: expect.objectContaining({ keyId: '99' }) }),
    )
  })

  it('shows every existing and newly supported client before the config workspace', async () => {
    const wrapper = await mountView()
    const expected = [
      'claude-code', 'codex-cli', 'qwen-code', 'gemini-cli', 'opencode', 'cline', 'roo-code',
      'cherry-studio', 'lobe-chat', 'chatbox', 'dify', 'curl', 'python',
    ]
    for (const id of expected) {
      expect(wrapper.find(`[data-tk="quickstart-client-${id}"]`).exists()).toBe(true)
    }
    const picker = wrapper.get('[data-tk="quickstart-client-picker"]')
    const workspace = wrapper.get('[data-tk="quickstart-config-workspace"]')
    expect(picker.element.compareDocumentPosition(workspace.element) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('selects Qwen Code at the tool layer and keeps protocol as a secondary control', async () => {
    const wrapper = await mountView()
    await wrapper.get('[data-tk="quickstart-client-qwen-code"]').trigger('click')
    await nextTick()
    expect(wrapper.get('[data-tk="quickstart-client-qwen-code"]').attributes('aria-pressed')).toBe('true')
    expect(wrapper.get('[data-tk="quickstart-protocol-picker"]').exists()).toBe(true)
    expect(wrapper.get('[data-tk="quickstart-protocol-anthropic"]').attributes('disabled')).toBeUndefined()
    expect(wrapper.get('[data-tk="quickstart-protocol-openai"]').attributes('disabled')).toBeUndefined()
    expect(wrapper.get('[data-test="use-key-guide"]').text()).toContain('|qwen-code|anthropic|')
  })

  it('keeps incompatible clients visible and disabled for a direct Anthropic key', async () => {
    listKeys.mockResolvedValue({
      items: [{
        ...universalKey(),
        routing_mode: 'direct',
        group_id: 1,
        group: { id: 1, name: 'Claude', platform: 'anthropic', claude_code_only: false },
      }],
      total: 1,
      page: 1,
      page_size: 100,
      pages: 1,
    })
    const wrapper = await mountView()
    const codex = wrapper.get('[data-tk="quickstart-client-codex-cli"]')
    expect(codex.attributes('data-unavailable')).toBe('true')
    expect(codex.text()).toContain('Codex CLI')
    await codex.trigger('click')
    await nextTick()
    expect(wrapper.get('[data-tk="quickstart-client-unavailable"]').text()).toBe('quickstart.unavailableProtocol')

    await wrapper.get('[data-tk="quickstart-client-qwen-code"]').trigger('click')
    await nextTick()
    expect(wrapper.get('[data-tk="quickstart-protocol-anthropic"]').attributes('disabled')).toBeUndefined()
    expect(wrapper.get('[data-tk="quickstart-protocol-openai"]').attributes('disabled')).toBe('')
  })

  it('keeps every non-Claude client visible but disabled for an explicitly CC-only group', async () => {
    listKeys.mockResolvedValue({
      items: [{
        ...universalKey(),
        routing_mode: 'direct',
        group_id: 1,
        group: { id: 1, name: 'Legacy CC-only', platform: 'anthropic', claude_code_only: true },
      }],
      total: 1,
      page: 1,
      page_size: 100,
      pages: 1,
    })
    const wrapper = await mountView()
    expect(wrapper.get('[data-tk="quickstart-client-claude-code"]').attributes('data-unavailable')).toBeUndefined()
    for (const id of ['qwen-code', 'opencode', 'curl', 'python']) {
      expect(wrapper.get(`[data-tk="quickstart-client-${id}"]`).attributes('data-unavailable')).toBe('true')
    }
  })

  it('writes the selected model back to the URL without exposing the API key', async () => {
    const wrapper = await mountView()
    wrapper.getComponent({ name: 'UseKeyGuide' }).vm.$emit('modelChange', 'gpt-5.5')
    await nextTick()
    expect(replaceMock).toHaveBeenLastCalledWith({
      query: expect.objectContaining({ model: 'gpt-5.5', keyId: '42' }),
    })
    expect(JSON.stringify(replaceMock.mock.calls.at(-1))).not.toContain('sk-universal-test-key')
  })
})
