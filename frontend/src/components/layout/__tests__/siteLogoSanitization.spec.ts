import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

const dir = dirname(fileURLToPath(import.meta.url))
const brandingSource = readFileSync(resolve(dir, '../../../utils/branding.ts'), 'utf8')
const sidebarSource = readFileSync(resolve(dir, '../AppSidebar.vue'), 'utf8')
const keyUsageViewSource = readFileSync(resolve(dir, '../../../views/KeyUsageView.vue'), 'utf8')
const homeLandingSource = readFileSync(resolve(dir, '../../../components/home/HomeTkLanding.tk.vue'), 'utf8')

describe('site_logo sanitization', () => {
  it('resolveSiteLogo is the single sanitizing fallback', () => {
    expect(brandingSource).toContain("import { sanitizeUrl } from '@/utils/url'")
    expect(brandingSource).toContain('allowRelative: true')
    expect(brandingSource).toContain('allowDataUrl: true')
    expect(brandingSource).toContain('DEFAULT_SITE_LOGO')
  })

  it('AppSidebar resolves site logos through resolveSiteLogo', () => {
    expect(sidebarSource).toContain("import { resolveSiteLogo } from '@/utils/branding'")
    expect(sidebarSource).toContain('resolveSiteLogo(appStore.siteLogo')
  })

  it('KeyUsageView resolves site logos through resolveSiteLogo', () => {
    expect(keyUsageViewSource).toContain("import { resolveSiteLogo } from '@/utils/branding'")
    expect(keyUsageViewSource).toContain('resolveSiteLogo(appStore.cachedPublicSettings?.site_logo || appStore.siteLogo')
  })

  it('HomeTkLanding resolves site logos through resolveSiteLogo', () => {
    expect(homeLandingSource).toContain("import { resolveSiteLogo } from '@/utils/branding'")
    expect(homeLandingSource).toContain('resolveSiteLogo(appStore.cachedPublicSettings?.site_logo || appStore.siteLogo')
  })
})
