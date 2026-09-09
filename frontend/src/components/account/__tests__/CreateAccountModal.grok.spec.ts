import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const source = readFileSync(
  resolve(process.cwd(), 'src/components/account/CreateAccountModal.vue'),
  'utf8'
)

describe('CreateAccountModal Grok account types', () => {
  it('offers API-key edge relay setup alongside OAuth with the official xAI fallback', () => {
    expect(source).toContain('data-testid="grok-account-type-api-key"')
    expect(source).toContain("@click=\"accountCategory = 'apikey'\"")
    expect(source).toContain('newPlatform === PLATFORM_GROK')
    expect(source).toContain("return 'https://api.x.ai/v1'")
    expect(source).toContain("const baseURL = apiKeyBaseUrl.value.trim()")
    expect(source).toContain("form.platform === 'grok'")
    expect(source).toContain("return 'xai-...'")
  })

  it('exposes custom upstream URL and header override for the OAuth create flow', () => {
    expect(source).toContain('data-testid="grok-custom-base-url-toggle"')
    expect(source).toContain('data-testid="grok-custom-base-url-input"')
    expect(source).toContain('form.platform === \'grok\' && isOAuthFlow')
  })

  it('validates and applies upstream config on all five Grok OAuth create paths', () => {
    // Direct form, authorization code, refresh token, SSO, and password login.
    expect(source.match(/validateGrokOAuthUpstreamConfig\(\)/g)?.length).toBe(5)
    expect(source).toContain('applyGrokOAuthUpstreamConfig(bundle.credentials)')
    expect(source.match(/applyGrokOAuthUpstreamConfig\(credentials\)/g)?.length).toBe(4)
  })
})
