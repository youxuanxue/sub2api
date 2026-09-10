import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const source = readFileSync(
  resolve(process.cwd(), 'src/components/account/CreateAccountModal.vue'),
  'utf8'
)

describe('CreateAccountModal Grok account types', () => {
  it('exposes custom upstream URL and header override for the OAuth create flow', () => {
    expect(source).toContain('data-testid="grok-custom-base-url-toggle"')
    expect(source).toContain('data-testid="grok-custom-base-url-input"')
    expect(source).toContain('form.platform === \'grok\' && isOAuthFlow')
  })

  it('validates and applies upstream config on all five Grok OAuth create paths', () => {
    // 表单直建 / 授权码兑换 / RT 批量 / SSO 批量 / 密码授权。
    expect(source.match(/validateGrokOAuthUpstreamConfig\(\)/g)?.length).toBe(5)
    expect(source).toContain('applyGrokOAuthUpstreamConfig(bundle.credentials)')
    expect(source.match(/applyGrokOAuthUpstreamConfig\(credentials\)/g)?.length).toBe(4)
  })
})
