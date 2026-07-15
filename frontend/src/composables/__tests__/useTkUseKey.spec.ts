import { afterEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import { useTkUseKey } from '@/composables/useTkUseKey'

function createUseKey() {
  return useTkUseKey({
    apiKeyId: ref(42),
    apiKey: ref('sk-tool-probe'),
    platform: ref('openai'),
    routingMode: ref('direct'),
    claudeCodeOnly: ref(false),
    baseRoot: ref('https://api.tokenkey.test'),
  })
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('useTkUseKey tool-call probe', () => {
  it('forces a side-effect-free function and verifies the returned tool call', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
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

    expect(fetchMock).toHaveBeenCalledOnce()
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('https://api.tokenkey.test/v1/chat/completions')
    const body = JSON.parse(String(init.body))
    expect(body.tools[0].function.name).toBe('tokenkey_quickstart_probe')
    expect(body.tool_choice).toEqual({
      type: 'function',
      function: { name: 'tokenkey_quickstart_probe' },
    })
    expect(tk.testState.value).toMatchObject({ status: 'ok', httpStatus: 200, toolCall: true })
  })

  it('does not treat a plain 200 response as a successful tool-call verification', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({
      choices: [{ message: { content: 'ok' } }],
    }), { status: 200 })))

    const tk = createUseKey()
    await tk.runTest('openai', { requireToolCall: true })

    expect(tk.testState.value).toMatchObject({
      status: 'error',
      httpStatus: 200,
      reason: 'missing_tool_call',
    })
  })
})
