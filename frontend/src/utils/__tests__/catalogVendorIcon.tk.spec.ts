import { describe, expect, it } from 'vitest'
import {
  formatCatalogVendorLabel,
  normalizeCatalogVendorSlug,
  resolveCatalogVendorIconKey,
} from '../catalogVendorIcon.tk'

describe('catalogVendorIcon.tk', () => {
  it.each(['google', 'Google', 'gemini', 'Antigravity', 'vertexai', 'vertex_ai', 'vertex_ai-language-models', 'Vertex_AI-video-models', 'vertex_ai-embedding-models'])(
    'groups %s under Google with the same label and icon', (vendor) => {
      expect(normalizeCatalogVendorSlug(vendor)).toBe('google')
      expect(formatCatalogVendorLabel(vendor)).toBe('Google')
      expect(resolveCatalogVendorIconKey(vendor)).toBe('gemini')
    },
  )

  it.each(['wenxin', 'qianfan', 'Baidu'])(
    'groups %s under Baidu with the same label and icon', (vendor) => {
      expect(normalizeCatalogVendorSlug(vendor)).toBe('baidu')
      expect(formatCatalogVendorLabel(vendor)).toBe('Baidu')
      expect(resolveCatalogVendorIconKey(vendor)).toBe('wenxin')
    },
  )

  it('maps common catalog vendors to icon keys', () => {
    expect(resolveCatalogVendorIconKey('OpenAI')).toBe('openai')
    expect(resolveCatalogVendorIconKey('vertex_ai-video-models')).toBe('gemini')
    expect(resolveCatalogVendorIconKey('volcengine')).toBe('doubao')
    expect(resolveCatalogVendorIconKey('xai')).toBe('xai')
    expect(resolveCatalogVendorIconKey('deepseek')).toBe('deepseek')
    expect(resolveCatalogVendorIconKey('antigravity')).toBe('gemini')
    expect(resolveCatalogVendorIconKey('wenxin')).toBe('wenxin')
  })

  it('formats friendly vendor labels for display', () => {
    expect(formatCatalogVendorLabel('vertex_ai')).toBe('Google')
    expect(formatCatalogVendorLabel('volcengine')).toBe('VolcEngine')
    expect(formatCatalogVendorLabel('antigravity')).toBe('Google')
    expect(formatCatalogVendorLabel('gemini')).toBe('Google')
    expect(formatCatalogVendorLabel('wenxin')).toBe('Baidu')
  })
})
