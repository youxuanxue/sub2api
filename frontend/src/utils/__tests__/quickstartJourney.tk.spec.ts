import { describe, expect, it } from 'vitest'
import { quickstartReturnPath, safeInternalRedirect } from '../quickstartJourney.tk'

describe('Quickstart authentication return path', () => {
  it('retains the selected protocol or transport with a local destination', () => {
    expect(safeInternalRedirect(quickstartReturnPath('qwen-code', 'openai', 'http')))
      .toBe('/quickstart?client=qwen-code&protocol=openai')
    expect(quickstartReturnPath('codex-cli', 'anthropic', 'websocket'))
      .toBe('/quickstart?client=codex-cli&transport=websocket')
  })
  it.each(['https://outside.test', '//outside.test', '/\\outside.test', '/\noutside.test', null, ['/keys']])(
    'rejects an unsafe or ambiguous destination: %s', value => {
      expect(safeInternalRedirect(value)).toBe('/quickstart')
      expect(safeInternalRedirect(value, '/dashboard')).toBe('/dashboard')
    },
  )
})
