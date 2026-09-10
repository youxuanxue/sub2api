import { readFileSync } from 'node:fs'
import { expect, test, type Page } from '@playwright/test'

const ownerRoot = new URL('../../backend/internal/service/', import.meta.url)
const registry = JSON.parse(readFileSync(new URL('tk_pricing_overlay.json', ownerRoot), 'utf8'))
const manifest = JSON.parse(readFileSync(new URL('tk_served_models.json', ownerRoot), 'utf8'))
const models = Object.entries(manifest.entries)
  .filter(([id, intent]) => (intent as { display: boolean }).display && ['tts', 'audio_transcription'].includes(registry[id]?.mode))
  .map(([id]) => ({
    model_id: id, vendor: registry[id].litellm_provider, capabilities: [],
    pricing: {
      currency: 'USD', input_per_1k_tokens: 0, output_per_1k_tokens: 0,
      billing_mode: registry[id].mode === 'tts' ? 'tts' : 'stt',
      output_cost_per_character: registry[id].output_cost_per_character,
      input_cost_per_second: registry[id].input_cost_per_second,
    },
  }))

async function mockCatalog(page: Page, data: unknown[]) {
  await page.addInitScript(() => localStorage.setItem('tokenkey_locale', 'zh'))
  await page.route('**/setup/status', route => route.fulfill({ json: { code: 0, data: { needs_setup: false } } }))
  await page.route('**/api/v1/**', route => {
    const path = new URL(route.request().url()).pathname
    if (path === '/api/v1/public/pricing') return route.fulfill({ json: { object: 'list', data, updated_at: '2026-09-09T00:00:00Z' } })
    if (path.startsWith('/api/v1/auth/')) return route.fulfill({ status: 401, json: { code: 401, message: 'Guest' } })
    return route.fulfill({ json: { code: 0, data: { pricing_catalog_public: true } } })
  })
}

for (const viewport of [{ width: 1440, height: 1000 }, { width: 390, height: 844 }]) {
  test(`audio prices and filters (${viewport.width}px)`, async ({ page }) => {
    expect(models.length).toBeGreaterThan(0)
    await page.setViewportSize(viewport)
    await mockCatalog(page, models)
    await page.goto('/models')
    await page.locator('[data-tk="models-marketplace-tab-audio"]').click()
    await expect(page.locator('[data-tk="catalog-audio-price"]')).toHaveCount(models.length)
    for (const model of models) {
      const card = page.locator(`[data-tk="models-marketplace-card-${model.model_id}"]`)
      await expect(card).toContainText(model.pricing.billing_mode === 'tts' ? '/ 万字符' : '/ 小时')
      await expect(card.locator('[data-tk="catalog-audio-price"] > span').first()).not.toHaveText('$0')
    }
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    await page.screenshot({ path: `/tmp/tk-volc-audio-cards-${viewport.width}.png`, fullPage: true })
    await page.locator('[data-tk="catalog-view-pricing"]').click()
    await expect(page.locator('[data-tk="cold-start-pricing-table"]')).toBeVisible()
    await expect(page.locator('[data-tk="catalog-audio-price"]')).toHaveCount(models.length)
    for (const model of models) {
      const row = page.locator('tr').filter({ has: page.getByText(model.model_id, { exact: true }) })
      await expect(row).toContainText(model.pricing.billing_mode === 'tts' ? '/ 万字符' : '/ 小时')
    }
    await page.screenshot({ path: `/tmp/tk-volc-audio-pricing-${viewport.width}.png`, fullPage: true })
  })

  test(`multimodal embedding prices (${viewport.width}px)`, async ({ page }) => {
    const id = 'doubao-embedding-vision'
    const price = registry[id]
    await page.setViewportSize(viewport)
    await mockCatalog(page, [{
      model_id: id, vendor: price.litellm_provider, capabilities: ['vision'],
      pricing: {
        currency: 'USD', billing_mode: 'embedding', output_per_1k_tokens: 0,
        input_per_1k_tokens: price.input_cost_per_token * 1000 * 1.06,
        input_cost_per_image_token: price.input_cost_per_image_token * 1.06,
      },
    }])
    await page.goto('/models')
    await page.locator('[data-tk="models-marketplace-tab-embedding"]').click()
    const prices = page.locator('[data-tk-embedding-price]')
    await expect(prices).toContainText('文本')
    await expect(prices).toContainText('图片')
    await expect(prices.locator('[data-tk="catalog-tier-price"]')).toHaveText(['$0.111', '$0.285'])
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    await page.screenshot({ path: `/tmp/tk-volc-embedding-cards-${viewport.width}.png`, fullPage: true })
    await page.locator('[data-tk="catalog-view-pricing"]').click()
    await expect(page.locator('[data-tk="cold-start-pricing-table"]')).toBeVisible()
    await expect(prices.locator('[data-tk="catalog-tier-price"]')).toHaveText(['$0.111', '$0.285'])
    await page.screenshot({ path: `/tmp/tk-volc-embedding-pricing-${viewport.width}.png`, fullPage: true })
  })
}
