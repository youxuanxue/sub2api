import { describe, expect, it } from 'vitest'
import {
  formatCatalogVendorLabel,
  normalizeCatalogVendorSlug,
  resolveCatalogVendorIconKey,
} from '../catalogVendorIcon.tk'

describe('catalogVendorIcon.tk', () => {
  it('normalizes vertex_ai modality suffixes to vertex_ai', () => {
    expect(normalizeCatalogVendorSlug('vertex_ai-language-models')).toBe('vertex_ai')
    expect(normalizeCatalogVendorSlug('vertex_ai')).toBe('vertex_ai')
  })

  it('maps common catalog vendors to icon keys', () => {
    expect(resolveCatalogVendorIconKey('OpenAI')).toBe('openai')
    expect(resolveCatalogVendorIconKey('vertex_ai-video-models')).toBe('gemini')
    expect(resolveCatalogVendorIconKey('volcengine')).toBe('doubao')
    expect(resolveCatalogVendorIconKey('xai')).toBe('xai')
    expect(resolveCatalogVendorIconKey('deepseek')).toBe('deepseek')
  })

  it('formats friendly vendor labels for display', () => {
    expect(formatCatalogVendorLabel('vertex_ai')).toBe('Vertex AI')
    expect(formatCatalogVendorLabel('volcengine')).toBe('VolcEngine')
  })
})
