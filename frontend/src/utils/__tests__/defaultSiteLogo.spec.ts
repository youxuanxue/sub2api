import { readdirSync, readFileSync, statSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

import { DEFAULT_SITE_LOGO, resolveSiteLogo } from '@/utils/branding'

const frontendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../../..')

const BRAND_SURFACES = [
  'src/components/layout/AppSidebar.vue',
  'src/components/layout/AuthLayout.vue',
  'src/components/modelPlaza/PlazaNavBar.vue',
  'src/views/public/LegalDocumentView.vue',
  'src/views/KeyUsageView.vue',
  'src/components/home/HomeTkLanding.tk.vue',
] as const

const UPSTREAM_LOGO_FALLBACK = /(?:\|\|\s*['"]\/logo\.svg['"]|href=["']\/logo\.svg["'])/

function walkSourceFiles(dir: string, acc: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    if (name === 'node_modules' || name === 'dist' || name === '__tests__') {
      continue
    }
    const full = join(dir, name)
    const stat = statSync(full)
    if (stat.isDirectory()) {
      walkSourceFiles(full, acc)
      continue
    }
    if (name.endsWith('.vue') || name.endsWith('.html') || name === 'App.vue' || name === 'main.ts') {
      acc.push(full)
    }
  }
  return acc
}

describe('resolveSiteLogo', () => {
  it('falls back to the TokenKey logo when no site_logo is configured', () => {
    expect(resolveSiteLogo('')).toBe('/logo.png')
    expect(resolveSiteLogo('')).toBe(DEFAULT_SITE_LOGO)
    expect(resolveSiteLogo('')).not.toBe('/logo.svg')
  })

  it('rejects an unsafe URL and still uses the TokenKey logo', () => {
    expect(resolveSiteLogo('javascript:alert(1)')).toBe(DEFAULT_SITE_LOGO)
  })

  it('keeps a sanitized custom logo', () => {
    expect(resolveSiteLogo('/uploads/custom.png')).toBe('/uploads/custom.png')
  })
})

describe('TokenKey default logo surfaces', () => {
  it('known brand surfaces resolve through resolveSiteLogo', () => {
    for (const rel of BRAND_SURFACES) {
      const src = readFileSync(join(frontendRoot, rel), 'utf8')
      expect(src, rel).toContain('resolveSiteLogo')
      expect(src, rel).not.toMatch(UPSTREAM_LOGO_FALLBACK)
    }
  })

  it('no Vue/HTML brand surface falls back to the upstream Sub2API logo', () => {
    const files = [
      ...walkSourceFiles(join(frontendRoot, 'src')),
      join(frontendRoot, 'index.html'),
    ]
    const offenders = files.filter((file) => UPSTREAM_LOGO_FALLBACK.test(readFileSync(file, 'utf8')))
    expect(offenders.map((file) => file.slice(frontendRoot.length + 1))).toEqual([])
  })
})
