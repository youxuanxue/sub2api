import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

/**
 * Regression guard for features_config.web_search_emulation:
 * the channel editor used to render and submit the anthropic dimension only,
 * so any UI save overwrote the other platform dimensions written by the API.
 * Render side lives in ChannelsView; submit side lives in channelFormConversion.
 */
describe('ChannelsView web_search_emulation platform whitelist', () => {
  const view = readFileSync(resolve('src/views/admin/ChannelsView.vue'), 'utf8')
  const conversion = readFileSync(resolve('src/utils/channelFormConversion.ts'), 'utf8')
  const zh = readFileSync(resolve('src/i18n/locales/zh/admin/channels.ts'), 'utf8')
  const en = readFileSync(resolve('src/i18n/locales/en/admin/channels.ts'), 'utf8')

  it('declares the whitelist aligned with the backend platform whitelist', () => {
    expect(view).toContain('const WEB_SEARCH_EMULATION_PLATFORMS: ReadonlySet<string> = new Set([')
    expect(conversion).toContain('export const WEB_SEARCH_EMULATION_PLATFORMS: ReadonlySet<string> = new Set([')
    for (const platform of ['anthropic', 'openai', 'command_code', 'opencode_go', 'minimax', 'zhipu']) {
      expect(view).toContain(`'${platform}',`)
      expect(conversion).toContain(`'${platform}',`)
    }
  })

  it('renders the toggle for every whitelisted platform section', () => {
    expect(view).toContain('WEB_SEARCH_EMULATION_PLATFORMS.has(section.platform) && webSearchGlobalEnabled')
    expect(view).not.toContain("section.platform === 'anthropic' && webSearchGlobalEnabled")
  })

  it('submits every whitelisted platform dimension on save', () => {
    const block =
      conversion.match(/Collect web_search_emulation[\s\S]*?delete featuresConfig\.web_search_emulation/)?.[0] ?? ''
    expect(block).toContain('WEB_SEARCH_EMULATION_PLATFORMS.has(section.platform)')
    expect(block).toContain('section.enabled')
    expect(block).toContain('!!section.web_search_emulation')
    expect(block).not.toContain('PLATFORM_ANTHROPIC')
  })

  it('i18n hint no longer hardcodes Anthropic as the only platform', () => {
    expect(zh).not.toContain('所有 Anthropic 分组的账号将自动拦截')
    expect(en).not.toContain('Anthropic groups will intercept')
    expect(zh).toContain('webSearchEmulationHint')
    expect(en).toContain('webSearchEmulationHint')
  })
})
