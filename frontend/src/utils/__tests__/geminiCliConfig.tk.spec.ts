import { describe, expect, it } from 'vitest'
import {
  buildAntigravityCliFiles,
  buildAntigravityCliSettingsJson,
  buildGeminiCliFiles,
  buildGeminiCliSettingsJson,
  selectGeminiCliModels,
} from '@/utils/geminiCliConfig.tk'

describe('selectGeminiCliModels', () => {
  it('places selectedModel first, keeps chat gemini* models, and filters out image/embedding/non-gemini models', () => {
    const models = selectGeminiCliModels('gemini-4-argon', [
      { id: 'gemini-3.8-flash', capabilities: ['tools'] },
      { id: 'gemini-4-argon', capabilities: ['thinking'] },
      { id: 'gemini-3.1-flash-image', capabilities: [] },
      { id: 'gemini-embedding-001', capabilities: [] },
      { id: 'imagen-4.0-generate-001', capabilities: [] },
      { id: 'veo-3.1', capabilities: [] },
      { id: 'claude-opus-4-8', capabilities: [] },
      { id: 'gemini-3-flash', capabilities: [] },
    ])

    expect(models).toEqual([
      'gemini-4-argon',
      'gemini-3.8-flash',
      'gemini-3-flash',
    ])
  })

  it('preserves preview placeholder YOUR_MODEL_ID when servableModels is empty', () => {
    expect(selectGeminiCliModels('YOUR_MODEL_ID', [])).toEqual(['YOUR_MODEL_ID'])
  })
})

describe('buildGeminiCliSettingsJson & buildGeminiCliFiles', () => {
  it('sets gemini-api-key auth, dynamicModelConfiguration, visible modelDefinitions, and contexts=[] resolutions', () => {
    const parsed = JSON.parse(buildGeminiCliSettingsJson('gemini-3.8-flash', [
      'gemini-3.8-flash',
      'gemini-4-argon',
      'gemini-2.5-flash-lite',
    ]))

    expect(parsed.security).toEqual({ auth: { selectedType: 'gemini-api-key' } })
    expect(parsed.model).toEqual({ name: 'gemini-3.8-flash' })
    expect(parsed.experimental).toEqual({ dynamicModelConfiguration: true })

    expect(parsed.modelConfigs.modelDefinitions['gemini-3.8-flash']).toEqual({
      displayName: 'gemini-3.8-flash',
      tier: 'flash',
      family: 'gemini-3',
      isPreview: false,
      isVisible: true,
      features: { thinking: true, multimodalToolUse: true },
    })
    expect(parsed.modelConfigs.modelDefinitions['gemini-4-argon']).toMatchObject({
      tier: 'pro',
      family: 'gemini-4',
      isPreview: false,
      isVisible: true,
    })
    expect(parsed.modelConfigs.modelDefinitions['gemini-2.5-flash-lite']).toMatchObject({
      tier: 'flash-lite',
      family: 'gemini-2.5',
      isPreview: false,
      isVisible: true,
    })
    expect(parsed.modelConfigs.modelIdResolutions['gemini-3.8-flash']).toEqual({
      default: 'gemini-3.8-flash',
      contexts: [],
    })
  })

  it('emits ~/.gemini/.env and ~/.gemini/settings.json on unix and %USERPROFILE% paths on Windows', () => {
    const unixFiles = buildGeminiCliFiles({
      baseUrl: 'https://api.tokenkey.dev/',
      apiKey: 'sk-tk-123',
      model: 'gemini-4-argon',
      servableModels: [{ id: 'gemini-4-argon', capabilities: [] }],
      os: 'unix',
    })
    expect(unixFiles.map((f) => f.path)).toEqual(['~/.gemini/.env', '~/.gemini/settings.json'])
    expect(unixFiles[0].content).toBe(
      'GOOGLE_GEMINI_BASE_URL="https://api.tokenkey.dev"\nGEMINI_API_KEY="sk-tk-123"\nGEMINI_MODEL="gemini-4-argon"',
    )

    const winFiles = buildGeminiCliFiles({
      baseUrl: 'https://api.tokenkey.dev',
      apiKey: 'sk-tk-123',
      model: 'gemini-4-argon',
      os: 'powershell',
    })
    expect(winFiles.map((f) => f.path)).toEqual([
      '%USERPROFILE%\\.gemini\\.env',
      '%USERPROFILE%\\.gemini\\settings.json',
    ])
  })
})

describe('buildAntigravityCliSettingsJson & buildAntigravityCliFiles', () => {
  it('sets modelProvider=gemini, bare modelName with TOOL_FORMATTER_TYPE_NONE, and maps built-in model labels', () => {
    const parsed = JSON.parse(buildAntigravityCliSettingsJson('gemini-4-argon', [
      'gemini-4-argon',
      'gemini-3-flash',
    ]))

    expect(parsed.modelProvider).toBe('gemini')
    expect(parsed.model).toBe('Gemini 4 Argon (High)')
    expect(parsed.customModelsConfig.customModels['gemini-4-argon']).toEqual({
      modelName: 'gemini-4-argon',
      label: 'gemini-4-argon',
      toolFormatterType: 'TOOL_FORMATTER_TYPE_NONE',
    })
    expect(parsed.customModelsConfig.customModels['gemini-3-flash']).toEqual({
      modelName: 'gemini-3-flash',
      label: 'gemini-3-flash',
      toolFormatterType: 'TOOL_FORMATTER_TYPE_NONE',
    })
  })

  it('omits top-level model for non-builtin custom models to avoid BYOM resolution failure in headless mode', () => {
    const parsed = JSON.parse(buildAntigravityCliSettingsJson('gemini-3-flash', ['gemini-3-flash']))
    expect(parsed.modelProvider).toBe('gemini')
    expect(parsed.model).toBeUndefined()
    expect(parsed.customModelsConfig.customModels['gemini-3-flash'].modelName).toBe('gemini-3-flash')
  })

  it('emits shell env vars per OS tab alongside ~/.gemini/antigravity-cli/settings.json', () => {
    const unixFiles = buildAntigravityCliFiles({
      baseUrl: 'https://api.tokenkey.dev/antigravity/',
      apiKey: 'sk-tk-123',
      model: 'gemini-3.8-flash',
      os: 'unix',
    })
    expect(unixFiles.map((f) => f.path)).toEqual([
      'Terminal',
      '~/.gemini/antigravity-cli/settings.json',
    ])
    expect(unixFiles[0].content).toBe(
      'export GOOGLE_GEMINI_BASE_URL="https://api.tokenkey.dev/antigravity"\nexport GEMINI_API_KEY="sk-tk-123"',
    )

    const cmdFiles = buildAntigravityCliFiles({
      baseUrl: 'https://api.tokenkey.dev',
      apiKey: 'sk-tk-123',
      model: 'gemini-3.8-flash',
      os: 'cmd',
    })
    expect(cmdFiles[0].path).toBe('Command Prompt')
    expect(cmdFiles[0].content).toContain('set GOOGLE_GEMINI_BASE_URL=https://api.tokenkey.dev')
    expect(cmdFiles[1].path).toBe('%USERPROFILE%\\.gemini\\antigravity-cli\\settings.json')

    const psFiles = buildAntigravityCliFiles({
      baseUrl: 'https://api.tokenkey.dev',
      apiKey: 'sk-tk-123',
      model: 'gemini-3.8-flash',
      os: 'powershell',
    })
    expect(psFiles[0].path).toBe('PowerShell')
    expect(psFiles[0].content).toContain('$env:GOOGLE_GEMINI_BASE_URL="https://api.tokenkey.dev"')
  })
})
