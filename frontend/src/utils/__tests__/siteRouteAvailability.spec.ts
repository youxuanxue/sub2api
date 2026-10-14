import { describe, expect, it } from 'vitest'
import { isSiteRouteAvailable } from '../siteRouteAvailability'

describe('domestic site routes', () => {
  it('rejects tool routes, aliases and nested routes on the domestic host', () => {
    for (const host of ['api-cn.tokenkey.dev', 'API-CN.tokenkey.dev.']) {
      for (const path of ['/studio', '/studio/?mode=chat', '/studio/image', '/playground', '/batch-image', '/docs/batch-image']) {
        expect(isSiteRouteAvailable(path, host)).toBe(false)
      }
    }
  })

  it('keeps keys, usage, login and operations available', () => {
    for (const path of ['/keys', '/usage', '/login', '/quickstart', '/admin/users', '/admin/ops']) {
      expect(isSiteRouteAvailable(path, 'api-cn.tokenkey.dev')).toBe(true)
    }
  })

  it('preserves tools on other sites and avoids prefix collisions', () => {
    for (const host of ['api.tokenkey.dev', 'tokenkey.dev', 'callmodel.io', 'localhost', 'api-cn.tokenkey.dev.example.com']) {
      expect(isSiteRouteAvailable('/studio', host)).toBe(true)
    }
    expect(isSiteRouteAvailable('/studio-help', 'api-cn.tokenkey.dev')).toBe(true)
  })
})
