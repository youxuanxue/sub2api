import { readFileSync } from 'node:fs'
import { expect, test } from '@playwright/test'

type Price = {
  mode?: string
  litellm_provider?: string
  input_cost_per_token?: number
  output_cost_per_token?: number
  max_input_tokens?: number
}
type Intent = { display: boolean; channel_type?: number }

const serviceRoot = new URL('../../backend/internal/service/', import.meta.url)
const registry: Record<string, Price> = JSON.parse(
  readFileSync(new URL('tk_pricing_overlay.json', serviceRoot), 'utf8'),
)
const manifest: { entries: Record<string, Intent> } = JSON.parse(
  readFileSync(new URL('tk_served_models.json', serviceRoot), 'utf8'),
)
// The backend contract test covers public filtering and retained settlement.
// This owner-derived fixture covers consumption by both real catalog views.
const selected = Object.entries(manifest.entries)
  .filter(([id, intent]) => intent.display && registry[id]?.mode === 'embedding')
  .map(([id]) => id)
  .sort()
const hidden = Object.entries(manifest.entries)
  .filter(([id, intent]) => !intent.display && registry[id]?.mode === 'embedding')
  .map(([id]) => id)
const catalog = {
  object: 'list',
  updated_at: '2026-09-09T11:03:00Z',
  data: selected.map((id) => ({
    model_id: id,
    vendor: registry[id].litellm_provider,
    context_window: registry[id].max_input_tokens,
    capabilities: [],
    pricing: {
      currency: 'USD',
      billing_mode: 'embedding',
      input_per_1k_tokens: (registry[id].input_cost_per_token ?? 0) * 1000,
      output_per_1k_tokens: 0,
    },
  })),
}

for (const viewport of [{ width: 1440, height: 1000 }, { width: 390, height: 844 }]) {
  test(`DashScope embedding cards and pricing table (${viewport.width}px)`, async ({ page }) => {
    test.setTimeout(60_000)
    expect(selected.length).toBeGreaterThan(0)
    await page.setViewportSize(viewport)
    await page.addInitScript(() => {
      localStorage.setItem('locale', 'zh-CN')
      localStorage.setItem('theme', 'dark')
    })
    await page.route('**/setup/status', (route) => route.fulfill({
      json: { code: 0, message: 'ok', data: { needs_setup: false, step: '' } },
    }))
    await page.route('**/api/v1/**', (route) => {
      const path = new URL(route.request().url()).pathname
      if (path === '/api/v1/public/pricing') {
        return route.fulfill({ json: catalog })
      }
      if (path.startsWith('/api/v1/auth/')) {
        return route.fulfill({ status: 401, json: { code: 401, message: 'Guest session' } })
      }
      return route.fulfill({
        json: { code: 0, message: 'ok', data: { pricing_catalog_public: true } },
      })
    })

    await page.goto('/models')
    await page.locator('[data-tk="models-marketplace-tab-embedding"]').click()
    const cards = page.locator('[data-tk^="models-marketplace-card-"]')
    await expect(cards).toHaveCount(selected.length)
    for (const id of selected) {
      await expect(page.locator(`[data-tk="models-marketplace-card-${id}"]`)).toContainText('dashscope')
    }
    for (const id of hidden) {
      await expect(page.getByText(id, { exact: true })).toHaveCount(0)
    }
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)

    await page.locator('[data-tk="catalog-view-pricing"]').click()
    await expect(page).toHaveURL(/view=pricing/)
    const table = page.locator('[data-tk="cold-start-pricing-table"]')
    await expect(table).toBeVisible()
    for (const id of selected) {
      await expect(table.getByText(id, { exact: true })).toHaveCount(1)
    }
    for (const id of hidden) {
      await expect(table.getByText(id, { exact: true })).toHaveCount(0)
    }
  })
}
