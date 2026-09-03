import { beforeEach, describe, expect, it } from 'vitest'

import { applyHomepageSeo } from '../homepageSeo.tk'

function meta(selector: string): string | null {
  return document.head.querySelector<HTMLMetaElement>(selector)?.content ?? null
}

describe('applyHomepageSeo', () => {
  beforeEach(() => {
    document.head.innerHTML = `
      <meta name="description" content="">
      <meta property="og:title" content="">
      <meta property="og:description" content="">
      <meta property="og:image" content="">
      <meta property="og:url" content="">
      <meta name="twitter:title" content="">
      <meta name="twitter:description" content="">
      <meta name="twitter:image" content="">
      <link rel="canonical" href="">
    `
  })

  it('publishes the apex homepage metadata for the current profile', () => {
    applyHomepageSeo('current')

    expect(document.title).toBe('TokenKey - AI API Gateway')
    expect(meta('meta[property="og:url"]')).toBe('https://tokenkey.dev/')
    expect(document.head.querySelector('link[rel="canonical"]')?.getAttribute('href')).toBe('https://tokenkey.dev/')
  })

  it('publishes distinct English metadata for the China model profile', () => {
    applyHomepageSeo('china-export')

    expect(document.title).toBe("TokenKey - China's Leading AI Models, One API")
    expect(meta('meta[name="description"]')).toContain('Seedance')
    expect(meta('meta[property="og:url"]')).toBe('https://global.tokenkey.dev/')
    expect(document.head.querySelector('link[rel="canonical"]')?.getAttribute('href')).toBe(
      'https://global.tokenkey.dev/',
    )
  })
})
