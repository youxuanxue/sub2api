import { afterEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'

const { getMePricingCatalogMock, getPublicPricingMock, getAPIKeyCapabilitiesMock } = vi.hoisted(() => ({
  getMePricingCatalogMock: vi.fn(),
  getPublicPricingMock: vi.fn(),
	getAPIKeyCapabilitiesMock: vi.fn(),
}))

vi.mock('@/api/me-pricing', () => ({
  getMePricingCatalog: (...args: unknown[]) => getMePricingCatalogMock(...args),
}))

vi.mock('@/api/pricing', () => ({
  getPublicPricing: (...args: unknown[]) => getPublicPricingMock(...args),
}))

vi.mock('@/api/api-key-capabilities', () => ({
	getAPIKeyCapabilities: (...args: unknown[]) => getAPIKeyCapabilitiesMock(...args),
}))

import { useTkUseKey, anthropicEnvModel, openaiCompatContextWindowEnvModel, claudeCodeEnvModel, formatProbeLatencyDetail } from '@/composables/useTkUseKey'

function createUseKey(apiKeyId = ref<number | null>(42), routingMode = ref<'direct' | 'universal'>('direct')) {
  return useTkUseKey({
    apiKeyId,
    apiKey: ref('sk-tool-probe'),
    platform: ref('openai'),
		routingMode,
    claudeCodeOnly: ref(false),
    baseRoot: ref('https://api.tokenkey.test'),
  })
}

afterEach(() => {
  vi.unstubAllGlobals()
  getMePricingCatalogMock.mockReset()
  getPublicPricingMock.mockReset()
	getAPIKeyCapabilitiesMock.mockReset()
})

describe('claudeCodeEnvModel helpers', () => {
  it('applies [1m] for opus-class Anthropic ids', () => {
    expect(anthropicEnvModel('claude-opus-4-8')).toBe('claude-opus-4-8[1m]')
    expect(anthropicEnvModel('claude-opus-4-8[1m]')).toBe('claude-opus-4-8[1m]')
    expect(anthropicEnvModel('claude-sonnet-4-6')).toBe('claude-sonnet-4-6')
  })

  it('applies [1m] for 1M OpenAI-compat dispatch ids', () => {
    expect(openaiCompatContextWindowEnvModel('gpt-5.5')).toBe('gpt-5.5[1m]')
    expect(openaiCompatContextWindowEnvModel('gpt-5.4')).toBe('gpt-5.4[1m]')
    expect(openaiCompatContextWindowEnvModel('gpt-5.5[1m]')).toBe('gpt-5.5[1m]')
    expect(openaiCompatContextWindowEnvModel('gpt-5.4-mini')).toBe('gpt-5.4-mini')
  })

  it('routes messages-dispatch Claude Code picks through the OpenAI helper', () => {
    expect(claudeCodeEnvModel('gpt-5.5', { openaiMessagesDispatch: true })).toBe('gpt-5.5[1m]')
    expect(claudeCodeEnvModel('claude-opus-4-8', { openaiMessagesDispatch: false })).toBe('claude-opus-4-8[1m]')
  })
})

describe('useTkUseKey model loading', () => {
	it('uses only the key capability SSOT for automatic routing and filters by protocol metadata', async () => {
		getAPIKeyCapabilitiesMock.mockResolvedValue({
			api_key_id: 42,
			routing_mode: 'universal',
			models: [
				{ id: 'qwen-through-messages', protocols: ['anthropic'], modalities: ['chat'], routes: [] },
				{ id: 'gpt-openai-only', protocols: ['openai'], modalities: ['chat'], routes: [] },
				{ id: 'gpt-codex-only', protocols: ['codex'], modalities: ['chat'], routes: [] },
				{ id: 'gpt-openai-and-codex', protocols: ['openai', 'codex'], modalities: ['chat'], routes: [] },
			],
		})
		const tk = createUseKey(ref(42), ref('universal'))

		await tk.loadModels()

		expect(getAPIKeyCapabilitiesMock).toHaveBeenCalledWith(42)
		expect(getMePricingCatalogMock).not.toHaveBeenCalled()
		expect(getPublicPricingMock).not.toHaveBeenCalled()
		expect(tk.modelsForFlavor('anthropic').map((model) => model.id)).toEqual(['qwen-through-messages'])
		expect(tk.modelsForFlavor('openai', 'openai').map((model) => model.id)).toEqual([
			'gpt-openai-only',
			'gpt-openai-and-codex',
		])
		expect(tk.modelsForFlavor('openai', 'codex').map((model) => model.id)).toEqual([
			'gpt-codex-only',
			'gpt-openai-and-codex',
		])
	})

	it('preserves capability load failure as an error state instead of an empty menu', async () => {
		getAPIKeyCapabilitiesMock.mockRejectedValue(new Error('capability unavailable'))
		const tk = createUseKey(ref(42), ref('universal'))

		await tk.loadModels()

		expect(tk.modelsError.value).toBe('capability unavailable')
		expect(tk.modelsLoaded.value).toBe(false)
		expect(tk.shouldWarnModelsEmpty('openai')).toBe(false)
	})

  it('applies a deep-linked model from the selected key live menu', async () => {
    getMePricingCatalogMock.mockResolvedValue({
      models: [{ model_id: 'claude-sonnet-live', capabilities: ['tools'] }],
    })
    const tk = createUseKey()

    await tk.loadModels()

    expect(tk.applyInitialModel('claude-sonnet-live')).toBe('anthropic')
    expect(tk.effectiveModel('anthropic')).toBe('claude-sonnet-live')
  })

  it('rejects an unknown deep-linked model and retains the live-menu fallback', async () => {
    getMePricingCatalogMock.mockResolvedValue({
      models: [{ model_id: 'gpt-live', capabilities: [] }],
    })
    const tk = createUseKey()

    await tk.loadModels()

    expect(tk.applyInitialModel('gpt-arbitrary-query')).toBeNull()
    expect(tk.effectiveModel('openai')).toBe('gpt-live')
  })

  it('rejects the previous key model after switching to a different live menu', async () => {
    getMePricingCatalogMock
      .mockResolvedValueOnce({ models: [{ model_id: 'gpt-key-one', capabilities: [] }] })
      .mockResolvedValueOnce({ models: [{ model_id: 'gpt-key-two', capabilities: [] }] })
    const apiKeyId = ref<number | null>(1)
    const tk = createUseKey(apiKeyId)

    await tk.loadModels()
    expect(tk.applyInitialModel('gpt-key-one')).toBe('openai')

    apiKeyId.value = 2
    await tk.loadModels()

    expect(tk.applyInitialModel('gpt-key-one')).toBeNull()
    expect(tk.effectiveModel('openai')).toBe('gpt-key-two')
  })

  it('ignores a stale model response after the selected key changes', async () => {
    let resolveFirst!: (value: { models: Array<{ model_id: string; capabilities: string[] }> }) => void
    let resolveSecond!: (value: { models: Array<{ model_id: string; capabilities: string[] }> }) => void
    const first = new Promise<{ models: Array<{ model_id: string; capabilities: string[] }> }>((resolve) => {
      resolveFirst = resolve
    })
    const second = new Promise<{ models: Array<{ model_id: string; capabilities: string[] }> }>((resolve) => {
      resolveSecond = resolve
    })
    getMePricingCatalogMock
      .mockReturnValueOnce(first)
      .mockReturnValueOnce(second)

    const apiKeyId = ref<number | null>(1)
    const tk = createUseKey(apiKeyId)
    const firstLoad = tk.loadModels()
    apiKeyId.value = 2
    const secondLoad = tk.loadModels()

    resolveSecond({ models: [{ model_id: 'gpt-key-two', capabilities: [] }] })
    await secondLoad
    resolveFirst({ models: [{ model_id: 'gpt-key-one', capabilities: [] }] })
    await firstLoad

    expect(tk.servableModels.value.map((model) => model.id)).toEqual(['gpt-key-two'])
    expect(tk.modelsLoaded.value).toBe(true)
    expect(tk.modelsLoading.value).toBe(false)
  })
})

describe('formatProbeLatencyDetail', () => {
  const t = (key: string, params?: Record<string, unknown>) => {
    if (key === 'quickstart.probeAuthMs') return `Auth ${params?.ms}ms`
    if (key === 'quickstart.probeModelMs') return `Model ${params?.ms}ms`
    if (key === 'quickstart.probeTotalMs') return `${params?.ms}ms total`
    if (key === 'keys.useKeyModal.testModelOk') return 'Model OK'
    if (key === 'keys.useKeyModal.testKeyValid') return 'Key valid'
    return key
  }

  it('shows auth and model latencies separately', () => {
    const detail = formatProbeLatencyDetail({
      status: 'ok',
      httpStatus: 200,
      authLatencyMs: 180,
      modelLatencyMs: 2400,
      latencyMs: 2580,
    }, t)
    expect(detail).toContain('Auth 180ms')
    expect(detail).toContain('Model 2400ms')
    expect(detail).toContain('2580ms total')
    expect(detail).toContain('Model OK')
  })
})

describe('useTkUseKey tool-call probe', () => {
	it('tests the model from the active Codex capability subset', async () => {
		getAPIKeyCapabilitiesMock.mockResolvedValue({
			api_key_id: 42,
			routing_mode: 'universal',
			models: [
				{ id: 'openai-only', protocols: ['openai'], modalities: ['chat'], routes: [] },
				{ id: 'codex-model', protocols: ['openai', 'codex'], modalities: ['chat'], routes: [] },
			],
		})
		const fetchMock = vi.fn()
			.mockResolvedValueOnce(new Response(JSON.stringify({ data: [] }), { status: 200 }))
			.mockResolvedValueOnce(new Response(JSON.stringify({
				choices: [{ message: { content: 'pong' } }],
			}), { status: 200 }))
		vi.stubGlobal('fetch', fetchMock)
		const tk = createUseKey(ref(42), ref('universal'))
		await tk.loadModels()

		await tk.runTest('openai', { protocol: 'codex' })

		const [, init] = fetchMock.mock.calls[1] as [string, RequestInit]
		expect(JSON.parse(String(init.body)).model).toBe('codex-model')
	})

  it('forces a side-effect-free function and verifies the returned tool call', async () => {
    vi.stubGlobal('window', { location: { origin: 'https://api.tokenkey.test', href: 'https://api.tokenkey.test/' } })
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ data: [] }), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        choices: [{
          message: {
            tool_calls: [{
              type: 'function',
              function: { name: 'tokenkey_quickstart_probe', arguments: '{"value":"ok"}' },
            }],
          },
        }],
      }), { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)

    const tk = createUseKey()
    await tk.runTest('openai', { requireToolCall: true })

    expect(fetchMock).toHaveBeenCalledTimes(2)
    const [authUrl] = fetchMock.mock.calls[0] as [string]
    expect(authUrl).toBe('https://api.tokenkey.test/v1/models')
    const [url, init] = fetchMock.mock.calls[1] as [string, RequestInit]
    expect(url).toBe('https://api.tokenkey.test/v1/chat/completions')
    const body = JSON.parse(String(init.body))
    expect(body.tools[0].function.name).toBe('tokenkey_quickstart_probe')
    expect(body.tool_choice).toEqual({
      type: 'function',
      function: { name: 'tokenkey_quickstart_probe' },
    })
    expect(tk.testState.value).toMatchObject({
      status: 'ok',
      httpStatus: 200,
      toolCall: true,
      authLatencyMs: expect.any(Number),
      modelLatencyMs: expect.any(Number),
      latencyMs: expect.any(Number),
    })
  })

  it('uses the configured gateway host for cross-origin browser fetch', async () => {
    vi.stubGlobal('window', { location: { origin: 'https://tokenkey.dev', href: 'https://tokenkey.dev/quickstart' } })
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ data: [] }), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        choices: [{ message: { content: 'pong' } }],
      }), { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)

    const tk = createUseKey()
    await tk.runTest('openai')

    expect(fetchMock).toHaveBeenCalledTimes(2)
    const [authUrl] = fetchMock.mock.calls[0] as [string]
    expect(authUrl).toBe('https://api.tokenkey.test/v1/models')
    const [url] = fetchMock.mock.calls[1] as [string]
    expect(url).toBe('https://api.tokenkey.test/v1/chat/completions')
    expect(tk.testState.value).toMatchObject({
      status: 'ok',
      authLatencyMs: expect.any(Number),
      modelLatencyMs: expect.any(Number),
    })
  })

  it('does not treat a plain 200 response as a successful tool-call verification', async () => {
    vi.stubGlobal('fetch', vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ data: [] }), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        choices: [{ message: { content: 'ok' } }],
      }), { status: 200 })))

    const tk = createUseKey()
    await tk.runTest('openai', { requireToolCall: true })

    expect(tk.testState.value).toMatchObject({
      status: 'error',
      httpStatus: 200,
      reason: 'missing_tool_call',
      authLatencyMs: expect.any(Number),
      modelLatencyMs: expect.any(Number),
    })
  })
})
