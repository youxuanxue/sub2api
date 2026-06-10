import { describe, expect, it } from 'vitest'
import {
  resolveTkClientIntegrationUrl,
  TK_CLIENT_INTEGRATIONS
} from '@/constants/clientIntegrations.tk'

const API_KEY = 'sk-tk-abc123'
const BASE_URL = 'https://api.tokenkey.dev'

function template(id: string): string {
  const entry = TK_CLIENT_INTEGRATIONS.find((c) => c.id === id)
  if (!entry) throw new Error(`missing integration: ${id}`)
  return entry.template
}

function decodeDataParam(url: string): Record<string, string> {
  const data = new URL(url).searchParams.get('data')
  if (!data) throw new Error('missing data param')
  return JSON.parse(atob(data)) as Record<string, string>
}

describe('resolveTkClientIntegrationUrl', () => {
  it('encodes Cherry Studio payload as base64 JSON with the key verbatim (no double sk- prefix)', () => {
    const url = resolveTkClientIntegrationUrl({
      template: template('cherry-studio'),
      apiKey: API_KEY,
      baseUrl: BASE_URL
    })
    expect(url.startsWith('cherrystudio://providers/api-keys?v=1&data=')).toBe(true)
    expect(decodeDataParam(url)).toEqual({ id: 'new-api', baseUrl: BASE_URL, apiKey: API_KEY })
  })

  it('encodes DeepChat payload with id new-api', () => {
    const url = resolveTkClientIntegrationUrl({
      template: template('deepchat'),
      apiKey: API_KEY,
      baseUrl: BASE_URL
    })
    expect(decodeDataParam(url)).toEqual({ id: 'new-api', baseUrl: BASE_URL, apiKey: API_KEY })
  })

  it('encodes AionUI payload with platform new-api', () => {
    const url = resolveTkClientIntegrationUrl({
      template: template('aionui'),
      apiKey: API_KEY,
      baseUrl: BASE_URL
    })
    expect(decodeDataParam(url)).toEqual({ platform: 'new-api', baseUrl: BASE_URL, apiKey: API_KEY })
  })

  it('substitutes {address}/{key} for query-param schemes (AMA / OpenCat)', () => {
    const ama = resolveTkClientIntegrationUrl({
      template: template('ama'),
      apiKey: API_KEY,
      baseUrl: BASE_URL
    })
    expect(ama).toBe(`ama://set-api-key?server=${encodeURIComponent(BASE_URL)}&key=${API_KEY}`)

    const opencat = resolveTkClientIntegrationUrl({
      template: template('opencat'),
      apiKey: API_KEY,
      baseUrl: BASE_URL
    })
    expect(opencat).toBe(`opencat://team/join?domain=${encodeURIComponent(BASE_URL)}&token=${API_KEY}`)
  })

  it('fills Lobe Chat web settings with encoded address and verbatim key', () => {
    const url = resolveTkClientIntegrationUrl({
      template: template('lobe-chat'),
      apiKey: API_KEY,
      baseUrl: BASE_URL
    })
    expect(url).toContain(`"apiKey":"${API_KEY}"`)
    expect(url).toContain(`"baseURL":"${encodeURIComponent(BASE_URL)}/v1"`)
    expect(url.startsWith('https://chat-preview.lobehub.com/?settings=')).toBe(true)
  })

  it('replaces every occurrence of repeated placeholders', () => {
    const url = resolveTkClientIntegrationUrl({
      template: 'app://x?a={key}&b={key}',
      apiKey: API_KEY,
      baseUrl: BASE_URL
    })
    expect(url).toBe(`app://x?a=${API_KEY}&b=${API_KEY}`)
  })
})
