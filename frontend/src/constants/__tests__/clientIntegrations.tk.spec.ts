import { describe, expect, it } from 'vitest'
import {
  resolveTkClientIntegrationUrl,
  TK_CLIENT_CATALOG,
  TK_CLIENT_INTEGRATIONS,
  TK_QUICKSTART_CLIENTS,
} from '@/constants/clientIntegrations.tk'

const API_KEY = 'sk-tk-abc123'
const BASE_URL = 'https://api.tokenkey.dev'

function template(id: string): string {
  const entry = TK_CLIENT_INTEGRATIONS.find((c) => c.id === id)
  if (!entry) throw new Error(`missing integration: ${id}`)
  return entry.template
}

function decodeParam(url: string, name = 'data'): Record<string, any> {
  const data = new URL(url).searchParams.get(name)
  if (!data) throw new Error('missing data param')
  return JSON.parse(atob(data)) as Record<string, any>
}

describe('resolveTkClientIntegrationUrl', () => {
  it('encodes Cherry Studio payload as base64 JSON with the key verbatim (no double sk- prefix)', () => {
    const url = resolveTkClientIntegrationUrl({
      template: template('cherry-studio'),
      apiKey: API_KEY,
      baseUrl: BASE_URL
    })
    expect(url.startsWith('cherrystudio://providers/api-keys?v=1&data=')).toBe(true)
    expect(decodeParam(url)).toEqual({
      id: 'tokenkey',
      name: 'TokenKey',
      type: 'openai',
      baseUrl: `${BASE_URL}/v1`,
      apiKey: API_KEY,
    })
  })

  it('encodes DeepChat payload with id new-api', () => {
    const url = resolveTkClientIntegrationUrl({
      template: template('deepchat'),
      apiKey: API_KEY,
      baseUrl: BASE_URL
    })
    expect(decodeParam(url)).toEqual({ id: 'new-api', baseUrl: BASE_URL, apiKey: API_KEY })
  })

  it('encodes AionUI payload with platform new-api', () => {
    const url = resolveTkClientIntegrationUrl({
      template: template('aionui'),
      apiKey: API_KEY,
      baseUrl: BASE_URL
    })
    expect(decodeParam(url)).toEqual({ platform: 'new-api', baseUrl: BASE_URL, apiKey: API_KEY })
  })

  it('encodes Chatbox host, chat path, key, and selected model in its local import contract', () => {
    const url = resolveTkClientIntegrationUrl({
      template: template('chatbox'),
      apiKey: API_KEY,
      baseUrl: `${BASE_URL}/v1`,
      model: 'gpt-5.5',
    })
    expect(url.startsWith('chatbox://provider/import?config=')).toBe(true)
    expect(decodeParam(url, 'config')).toEqual({
      id: 'tokenkey',
      name: 'TokenKey',
      type: 'openai',
      settings: {
        apiHost: BASE_URL,
        apiPath: '/v1/chat/completions',
        apiKey: API_KEY,
        models: [{ modelId: 'gpt-5.5' }],
      },
    })
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

  it('opens LobeHub with base URL only and never sends the key to its HTTPS origin', () => {
    const url = resolveTkClientIntegrationUrl({
      template: template('lobe-chat'),
      apiKey: API_KEY,
      baseUrl: BASE_URL
    })
    expect(url).not.toContain(API_KEY)
    expect(url).not.toContain('apiKey')
    expect(url).toContain(`"baseURL":"${encodeURIComponent(BASE_URL)}/v1"`)
    expect(url.startsWith('https://lobehub.com/?settings=')).toBe(true)
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

describe('TokenKey client catalog', () => {
  it('has unique ids and one owner for Quickstart and Studio integrations', () => {
    expect(new Set(TK_CLIENT_CATALOG.map((client) => client.id)).size).toBe(TK_CLIENT_CATALOG.length)
    expect(TK_CLIENT_INTEGRATIONS.every((integration) =>
      TK_CLIENT_CATALOG.some((client) => client.id === integration.id && client.surfaces.includes('studio')),
    )).toBe(true)
  })

  it('keeps every promised first-screen client visible from the shared catalog', () => {
    expect(TK_QUICKSTART_CLIENTS.map((client) => client.id).sort()).toEqual([
      'chatbox',
      'cherry-studio',
      'claude-code',
      'cline',
      'codex-cli',
      'curl',
      'dify',
      'gemini-cli',
      'lobe-chat',
      'opencode',
      'python',
      'qwen-code',
      'roo-code',
    ])
  })

  it('owns Quickstart behavior for every visible client', () => {
    for (const client of TK_QUICKSTART_CLIENTS) {
      expect(client.guideMode, `${client.id} guideMode`).toBeTruthy()
      expect(typeof client.usesEnvironmentPicker, `${client.id} usesEnvironmentPicker`).toBe('boolean')
    }
    expect(new Set(TK_QUICKSTART_CLIENTS.map((client) => client.guideId)).size)
      .toBe(TK_QUICKSTART_CLIENTS.length)
  })

  it('never resolves an HTTPS integration URL containing the API key', () => {
    const webIntegrations = TK_CLIENT_INTEGRATIONS.filter((integration) => integration.kind === 'web')
    expect(webIntegrations.length).toBeGreaterThan(0)
    for (const integration of webIntegrations) {
      const url = resolveTkClientIntegrationUrl({
        template: integration.template,
        apiKey: API_KEY,
        baseUrl: BASE_URL,
      })
      expect(url.startsWith('https://')).toBe(true)
      expect(url).not.toContain(API_KEY)
    }
  })
})
