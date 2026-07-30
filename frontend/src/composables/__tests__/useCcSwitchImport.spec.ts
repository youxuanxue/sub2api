import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import { executeCcSwitchImport } from '@/composables/useCcSwitchImport'
import type { ApiKey } from '@/types'

const baseKey: ApiKey = {
  id: 1,
  name: 'trial',
  key: 'sk-test-key',
  status: 'active',
  group_id: 1,
  group: {
    id: 1,
    name: 'anthropic',
    platform: 'anthropic',
  },
} as ApiKey

describe('executeCcSwitchImport', () => {
  beforeEach(() => {
    vi.stubGlobal('open', vi.fn())
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    vi.useRealTimers()
  })

  it('opens a ccswitch deeplink with the explicit ccsApp from Quickstart', () => {
    const onNotInstalled = vi.fn()
    executeCcSwitchImport(
      {
        key: baseKey,
        ccsApp: 'claude',
        baseUrl: 'https://api.tokenkey.dev',
        providerName: 'TokenKey',
      },
      onNotInstalled,
    )

    expect(window.open).toHaveBeenCalledTimes(1)
    const url = String(vi.mocked(window.open).mock.calls[0][0])
    expect(url.startsWith('ccswitch://v1/import?')).toBe(true)
    expect(new URLSearchParams(url.split('?')[1]).get('app')).toBe('claude')
  })

  it('calls onNotInstalled when the protocol handler does not take focus', () => {
    vi.spyOn(document, 'hasFocus').mockReturnValue(true)
    const onNotInstalled = vi.fn()
    executeCcSwitchImport(
      {
        key: { ...baseKey, routing_mode: 'universal', group: null, group_id: null },
        ccsApp: 'codex',
        baseUrl: 'https://api.tokenkey.dev',
        providerName: 'TokenKey',
      },
      onNotInstalled,
    )

    vi.advanceTimersByTime(100)
    expect(onNotInstalled).toHaveBeenCalledTimes(1)
  })
})
