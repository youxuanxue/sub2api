// TokenKey client catalog and external-client import integrations.
//
// Mirrors new-api's chat-link deeplinks (web/default/src/features/chat/lib/chat-links.ts
// `resolveChatUrl` + classic BUILTIN_TEMPLATES) so clients that already understand the
// new-api import payloads accept TokenKey unchanged. Two deliberate differences:
//   1. TokenKey API keys are stored WITH their `sk-` prefix (api_key_service.go), so the
//      key is passed through verbatim — never prepend another `sk-`.
//   2. DeepChat / AionUI keep the `new-api` identifier required by their import
//      flows. Cherry uses a distinct `tokenkey` provider id so importing cannot
//      overwrite an existing built-in New API provider.
//
// Placeholders:
//   {cherryConfig}/{aionuiConfig}/{deepchatConfig}/{chatboxConfig}
//     → encodeURIComponent(base64(JSON payload))
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
  /** Whether the launch payload carries the key locally into the client. */
  carriesApiKey: boolean
}

export type TkClientCategory = 'coding' | 'apps' | 'build'
export type TkClientSupportTier = 'verified' | 'import' | 'compatible'
export type TkClientAction = 'copy-config' | 'app-deeplink' | 'copy-fields'
export type TkClientGuideMode = 'native' | 'qwen' | 'openai-fields' | 'raw'

export interface TkClientCatalogEntry {
  id: string
  name: string
  category: TkClientCategory
  sortOrder: number
  icon: 'terminal' | 'sparkles' | 'chatBubble' | 'cube' | 'server'
  guideId: string
  supportTier: TkClientSupportTier
  action: TkClientAction
  protocols: Array<'anthropic' | 'openai' | 'gemini'>
  docsUrl: string
  surfaces: Array<'quickstart' | 'studio'>
  template?: string
  kind?: 'app' | 'web'
  secretTransport: 'config' | 'env-file' | 'local-scheme' | 'secret-ui' | 'none'
  guideMode?: TkClientGuideMode
  usesEnvironmentPicker?: boolean
}

/**
 * Single source of truth for every client TokenKey presents to users.
 *
 * `verified` means repository unit tests and browser smoke continuously
 * exercise the generated client-specific contract. `import` means the client
 * publishes a local URL scheme that TokenKey can populate. `compatible` is
 * deliberately weaker: only the standard protocol contract is asserted.
 */
export const TK_CLIENT_CATALOG: TkClientCatalogEntry[] = [
  {
    id: 'claude-code',
    name: 'Claude Code',
    category: 'coding',
    sortOrder: 10,
    icon: 'terminal',
    guideId: 'claude',
    supportTier: 'verified',
    action: 'copy-config',
    protocols: ['anthropic'],
    docsUrl: 'https://code.claude.com/docs/en/overview',
    surfaces: ['quickstart'],
    secretTransport: 'config',
    guideMode: 'native',
    usesEnvironmentPicker: true
  },
  {
    id: 'codex-cli',
    name: 'Codex CLI',
    category: 'coding',
    sortOrder: 20,
    icon: 'terminal',
    guideId: 'codex',
    supportTier: 'verified',
    action: 'copy-config',
    protocols: ['openai'],
    docsUrl: 'https://developers.openai.com/codex/cli',
    surfaces: ['quickstart'],
    secretTransport: 'config',
    guideMode: 'native',
    usesEnvironmentPicker: true
  },
  {
    id: 'qwen-code',
    name: 'Qwen Code',
    category: 'coding',
    sortOrder: 30,
    icon: 'terminal',
    guideId: 'qwen-code',
    supportTier: 'compatible',
    action: 'copy-config',
    protocols: ['anthropic', 'openai'],
    docsUrl: 'https://qwenlm.github.io/qwen-code-docs/en/users/configuration/model-providers',
    surfaces: ['quickstart'],
    secretTransport: 'env-file',
    guideMode: 'qwen',
    usesEnvironmentPicker: true
  },
  {
    id: 'gemini-cli',
    name: 'Gemini CLI',
    category: 'coding',
    sortOrder: 40,
    icon: 'sparkles',
    guideId: 'gemini',
    supportTier: 'verified',
    action: 'copy-config',
    protocols: ['gemini'],
    docsUrl: 'https://github.com/google-gemini/gemini-cli',
    surfaces: ['quickstart'],
    secretTransport: 'config',
    guideMode: 'native',
    usesEnvironmentPicker: true
  },
  {
    id: 'opencode',
    name: 'OpenCode',
    category: 'coding',
    sortOrder: 50,
    icon: 'terminal',
    guideId: 'opencode',
    supportTier: 'verified',
    action: 'copy-config',
    protocols: ['anthropic', 'openai', 'gemini'],
    docsUrl: 'https://opencode.ai/docs/providers',
    surfaces: ['quickstart'],
    secretTransport: 'config',
    guideMode: 'native',
    usesEnvironmentPicker: false
  },
  {
    id: 'cline',
    name: 'Cline',
    category: 'coding',
    sortOrder: 60,
    icon: 'terminal',
    guideId: 'cline',
    supportTier: 'compatible',
    action: 'copy-fields',
    protocols: ['openai'],
    docsUrl: 'https://docs.cline.bot/provider-config/openai-compatible',
    surfaces: ['quickstart'],
    secretTransport: 'secret-ui',
    guideMode: 'openai-fields',
    usesEnvironmentPicker: false
  },
  {
    id: 'roo-code',
    name: 'Roo Code',
    category: 'coding',
    sortOrder: 70,
    icon: 'terminal',
    guideId: 'roo-code',
    supportTier: 'compatible',
    action: 'copy-fields',
    protocols: ['openai'],
    docsUrl: 'https://docs.roocode.com/providers/openai-compatible',
    surfaces: ['quickstart'],
    secretTransport: 'secret-ui',
    guideMode: 'openai-fields',
    usesEnvironmentPicker: false
  },
  {
    id: 'cherry-studio',
    name: 'Cherry Studio',
    category: 'apps',
    sortOrder: 10,
    icon: 'chatBubble',
    guideId: 'cherry-studio',
    supportTier: 'import',
    action: 'app-deeplink',
    protocols: ['openai'],
    docsUrl: 'https://docs.cherry-ai.com',
    surfaces: ['quickstart', 'studio'],
    template: 'cherrystudio://providers/api-keys?v=1&data={cherryConfig}',
    kind: 'app',
    secretTransport: 'local-scheme',
    guideMode: 'openai-fields',
    usesEnvironmentPicker: false
  },
  {
    id: 'lobe-chat',
    name: 'LobeHub',
    category: 'apps',
    sortOrder: 20,
    icon: 'chatBubble',
    guideId: 'lobe-chat',
    supportTier: 'compatible',
    action: 'copy-fields',
    protocols: ['openai'],
    docsUrl: 'https://lobehub.com/docs/usage/providers/openai',
    surfaces: ['quickstart', 'studio'],
    template: 'https://lobehub.com/?settings={"keyVaults":{"openai":{"baseURL":"{address}/v1"}}}',
    kind: 'web',
    secretTransport: 'secret-ui',
    guideMode: 'openai-fields',
    usesEnvironmentPicker: false
  },
  {
    id: 'chatbox',
    name: 'Chatbox',
    category: 'apps',
    sortOrder: 30,
    icon: 'chatBubble',
    guideId: 'chatbox',
    supportTier: 'import',
    action: 'app-deeplink',
    protocols: ['openai'],
    docsUrl: 'https://chatboxai.app',
    surfaces: ['quickstart', 'studio'],
    template: 'chatbox://provider/import?config={chatboxConfig}',
    kind: 'app',
    secretTransport: 'local-scheme',
    guideMode: 'openai-fields',
    usesEnvironmentPicker: false
  },
  {
    id: 'dify',
    name: 'Dify',
    category: 'build',
    sortOrder: 10,
    icon: 'cube',
    guideId: 'dify',
    supportTier: 'compatible',
    action: 'copy-fields',
    protocols: ['openai'],
    docsUrl: 'https://docs.dify.ai/en/use-dify/workspace/model-providers',
    surfaces: ['quickstart'],
    secretTransport: 'secret-ui',
    guideMode: 'openai-fields',
    usesEnvironmentPicker: false
  },
  {
    id: 'curl',
    name: 'cURL',
    category: 'build',
    sortOrder: 20,
    icon: 'terminal',
    guideId: 'curl',
    supportTier: 'verified',
    action: 'copy-config',
    protocols: ['anthropic', 'openai', 'gemini'],
    docsUrl: 'https://curl.se/docs/',
    surfaces: ['quickstart'],
    secretTransport: 'config',
    guideMode: 'raw',
    usesEnvironmentPicker: false
  },
  {
    id: 'python',
    name: 'Python',
    category: 'build',
    sortOrder: 30,
    icon: 'terminal',
    guideId: 'python',
    supportTier: 'verified',
    action: 'copy-config',
    protocols: ['anthropic', 'openai', 'gemini'],
    docsUrl: 'https://www.python.org',
    surfaces: ['quickstart'],
    secretTransport: 'config',
    guideMode: 'raw',
    usesEnvironmentPicker: false
  },
  {
    id: 'deepchat',
    name: 'DeepChat',
    category: 'apps',
    sortOrder: 90,
    icon: 'chatBubble',
    guideId: 'deepchat',
    supportTier: 'import',
    action: 'app-deeplink',
    protocols: ['openai'],
    docsUrl: 'https://deepchat.thinkinai.xyz',
    surfaces: ['studio'],
    template: 'deepchat://provider/install?v=1&data={deepchatConfig}',
    kind: 'app',
    secretTransport: 'local-scheme'
  },
  {
    id: 'aionui',
    name: 'AionUI',
    category: 'apps',
    sortOrder: 100,
    icon: 'chatBubble',
    guideId: 'aionui',
    supportTier: 'import',
    action: 'app-deeplink',
    protocols: ['openai'],
    docsUrl: 'https://github.com/iOfficeAI/AionUi',
    surfaces: ['studio'],
    template: 'aionui://provider/add?v=1&data={aionuiConfig}',
    kind: 'app',
    secretTransport: 'local-scheme'
  },
  {
    id: 'ama',
    name: 'AMA 问天',
    category: 'apps',
    sortOrder: 110,
    icon: 'chatBubble',
    guideId: 'ama',
    supportTier: 'import',
    action: 'app-deeplink',
    protocols: ['openai'],
    docsUrl: 'https://github.com/idootop/mi-gpt',
    surfaces: ['studio'],
    template: 'ama://set-api-key?server={address}&key={key}',
    kind: 'app',
    secretTransport: 'local-scheme'
  },
  {
    id: 'opencat',
    name: 'OpenCat',
    category: 'apps',
    sortOrder: 120,
    icon: 'chatBubble',
    guideId: 'opencat',
    supportTier: 'import',
    action: 'app-deeplink',
    protocols: ['openai'],
    docsUrl: 'https://opencat.app',
    surfaces: ['studio'],
    template: 'opencat://team/join?domain={address}&token={key}',
    kind: 'app',
    secretTransport: 'local-scheme'
  }
]

export const TK_QUICKSTART_CLIENTS = TK_CLIENT_CATALOG
  .filter((client) => client.surfaces.includes('quickstart'))
  .sort((a, b) => a.sortOrder - b.sortOrder)

export const TK_CLIENT_INTEGRATIONS: TkClientIntegration[] = TK_CLIENT_CATALOG
  .filter((client): client is TkClientCatalogEntry & Required<Pick<TkClientCatalogEntry, 'template' | 'kind'>> =>
    client.surfaces.includes('studio') && Boolean(client.template && client.kind),
  )
  .map(({ id, name, template, kind, secretTransport }) => ({
    id,
    name,
    template,
    kind,
    carriesApiKey: secretTransport === 'local-scheme',
  }))

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

function encodeConfig(payload: Record<string, unknown>): string {
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
  /** selected model id, required by clients whose import contract carries a model list */
  model?: string
}

/**
 * Fill an integration template with the user's key + gateway address.
 * Same branch order as new-api `resolveChatUrl`: config placeholders first
 * (whole-payload base64 JSON), then generic {address}/{key} substitution.
 */
export function resolveTkClientIntegrationUrl({ template, apiKey, baseUrl, model }: ResolveTkIntegrationParams): string {
  let url = template
  const baseRoot = baseUrl.replace(/\/v1\/?$/, '').replace(/\/+$/, '')
  const apiBase = `${baseRoot}/v1`

  if (url.includes('{cherryConfig}')) {
    return replaceToken(url, '{cherryConfig}', encodeConfig({
      id: 'tokenkey',
      name: 'TokenKey',
      type: 'openai',
      baseUrl: apiBase,
      apiKey
    }))
  }
  if (url.includes('{deepchatConfig}')) {
    return replaceToken(url, '{deepchatConfig}', encodeConfig({ id: 'new-api', baseUrl, apiKey }))
  }
  if (url.includes('{aionuiConfig}')) {
    return replaceToken(url, '{aionuiConfig}', encodeConfig({ platform: 'new-api', baseUrl, apiKey }))
  }
  if (url.includes('{chatboxConfig}')) {
    return replaceToken(url, '{chatboxConfig}', encodeConfig({
      id: 'tokenkey',
      name: 'TokenKey',
      type: 'openai',
      settings: {
        apiHost: baseRoot,
        apiPath: '/v1/chat/completions',
        apiKey,
        models: model ? [{ modelId: model }] : []
      }
    }))
  }

  url = replaceToken(url, '{address}', encodeURIComponent(baseRoot))
  url = replaceToken(url, '{key}', apiKey)
  return url
}
