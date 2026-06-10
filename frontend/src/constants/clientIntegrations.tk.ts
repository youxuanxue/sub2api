// TokenKey-only external-client one-click integrations (Cherry Studio, DeepChat, …).
//
// Mirrors new-api's chat-link deeplinks (web/default/src/features/chat/lib/chat-links.ts
// `resolveChatUrl` + classic BUILTIN_TEMPLATES) so clients that already understand the
// new-api import payloads accept TokenKey unchanged. Two deliberate differences:
//   1. TokenKey API keys are stored WITH their `sk-` prefix (api_key_service.go), so the
//      key is passed through verbatim — never prepend another `sk-`.
//   2. The provider `id`/`platform` stays `new-api`: Cherry Studio / DeepChat / AionUI
//      key off that identifier for their import flow; TokenKey is wire-compatible.
//
// Placeholders:
//   {cherryConfig}/{aionuiConfig}/{deepchatConfig} → encodeURIComponent(base64(JSON payload))
//   {address} → encodeURIComponent(gateway base URL), {key} → API key verbatim

export interface TkClientIntegration {
  /** stable id, also the v-for :key */
  id: string
  /** display name (product name, not translated) */
  name: string
  /** deeplink / URL template, see placeholder contract above */
  template: string
  /** app = custom URL scheme (client must be installed); web = plain https link */
  kind: 'app' | 'web'
}

export const TK_CLIENT_INTEGRATIONS: TkClientIntegration[] = [
  {
    id: 'cherry-studio',
    name: 'Cherry Studio',
    template: 'cherrystudio://providers/api-keys?v=1&data={cherryConfig}',
    kind: 'app'
  },
  {
    id: 'deepchat',
    name: 'DeepChat',
    template: 'deepchat://provider/install?v=1&data={deepchatConfig}',
    kind: 'app'
  },
  {
    id: 'aionui',
    name: 'AionUI',
    template: 'aionui://provider/add?v=1&data={aionuiConfig}',
    kind: 'app'
  },
  {
    id: 'lobe-chat',
    name: 'Lobe Chat',
    template:
      'https://chat-preview.lobehub.com/?settings={"keyVaults":{"openai":{"apiKey":"{key}","baseURL":"{address}/v1"}}}',
    kind: 'web'
  },
  {
    id: 'ama',
    name: 'AMA 问天',
    template: 'ama://set-api-key?server={address}&key={key}',
    kind: 'app'
  },
  {
    id: 'opencat',
    name: 'OpenCat',
    template: 'opencat://team/join?domain={address}&token={key}',
    kind: 'app'
  }
]

/** UTF-8-safe base64 (payloads are ASCII today; keep names/ids safe anyway). */
function toBase64(value: string): string {
  if (typeof TextEncoder !== 'undefined') {
    const bytes = new TextEncoder().encode(value)
    let binary = ''
    bytes.forEach((b) => {
      binary += String.fromCharCode(b)
    })
    return btoa(binary)
  }
  return btoa(value)
}

function encodeConfig(payload: Record<string, string>): string {
  return encodeURIComponent(toBase64(JSON.stringify(payload)))
}

/** replaceAll without requiring lib es2021 (frontend tsconfig targets earlier). */
function replaceToken(input: string, token: string, value: string): string {
  return input.split(token).join(value)
}

export interface ResolveTkIntegrationParams {
  template: string
  /** full TokenKey API key, already carrying its sk- prefix */
  apiKey: string
  /** gateway base URL without trailing slash (resolveGatewayBaseUrl output) */
  baseUrl: string
}

/**
 * Fill an integration template with the user's key + gateway address.
 * Same branch order as new-api `resolveChatUrl`: config placeholders first
 * (whole-payload base64 JSON), then generic {address}/{key} substitution.
 */
export function resolveTkClientIntegrationUrl({ template, apiKey, baseUrl }: ResolveTkIntegrationParams): string {
  let url = template

  if (url.includes('{cherryConfig}')) {
    return replaceToken(url, '{cherryConfig}', encodeConfig({ id: 'new-api', baseUrl, apiKey }))
  }
  if (url.includes('{deepchatConfig}')) {
    return replaceToken(url, '{deepchatConfig}', encodeConfig({ id: 'new-api', baseUrl, apiKey }))
  }
  if (url.includes('{aionuiConfig}')) {
    return replaceToken(url, '{aionuiConfig}', encodeConfig({ platform: 'new-api', baseUrl, apiKey }))
  }

  url = replaceToken(url, '{address}', encodeURIComponent(baseUrl))
  url = replaceToken(url, '{key}', apiKey)
  return url
}
