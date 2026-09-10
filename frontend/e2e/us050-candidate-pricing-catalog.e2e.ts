import { expect, test, type Page } from '@playwright/test'
import type { MePricingCatalogResponse, MePricingModel } from '../src/api/me-pricing'

const USER = {
  id: 7,
  username: 'us050-e2e',
  email: 'us050-e2e@tokenkey.test',
  role: 'user',
  balance: 100,
  concurrency: 5,
  status: 'active',
  allowed_groups: null,
  balance_notify_enabled: false,
  balance_notify_threshold: null,
  balance_notify_extra_emails: [],
  onboarding_tour_seen_at: '2026-09-10T00:00:00Z',
  created_at: '2026-09-10T00:00:00Z',
  updated_at: '2026-09-10T00:00:00Z',
}

const GROUP = {
  id: 11,
  name: 'OpenAI billing origin',
  platform: 'openai',
  rate_multiplier: 1,
  list_multiplier: 1,
  has_override: false,
  is_exclusive: false,
  is_current_for_key: true,
  subscription_type: 'standard',
}

const CROSS_PLATFORM_MODEL = 'claude-opus-4-8'
const ALIAS = 'claude-opus-4-8-thinking'
const UNIVERSAL_ONLY_MODEL = 'gpt-5.5'
const RETIRED_MODEL = 'claude-3-5-sonnet-20241022'

function model(modelId: string): MePricingModel {
  return {
    model_id: modelId,
    vendor: modelId.startsWith('claude-') ? 'anthropic' : 'openai',
    billing_mode: 'token',
    your_price: { currency: 'USD', input_per_1k: 0.005, output_per_1k: 0.025 },
    capabilities: ['tools'],
    authorized_groups: [GROUP],
  }
}

function catalog(universal: boolean): MePricingCatalogResponse {
  const models = [model(CROSS_PLATFORM_MODEL), model(ALIAS)]
  if (universal) models.push(model(UNIVERSAL_ONLY_MODEL))
  return {
    target_group: universal ? null : GROUP,
    models,
    my_keys: [
      { id: 42, name: 'Direct candidate key', group_id: GROUP.id, group_name: GROUP.name, routing_mode: 'direct' },
      { id: 43, name: 'Universal candidate key', group_id: null, routing_mode: 'universal' },
    ],
    accessible_groups: [GROUP],
    authorized_groups_by_model: Object.fromEntries(models.map((row) => [row.model_id, row.authorized_groups!])),
    updated_at: '2026-09-10T00:00:00Z',
  }
}

// The fixture isolates the UI contract. Backend candidate eligibility is covered by Go tests.
async function installFixture(page: Page, requests: string[]): Promise<void> {
  const ok = (data: unknown) => ({ code: 0, message: 'success', data })
  await page.addInitScript((user) => {
    localStorage.setItem('auth_token', 'us050-e2e-token')
    localStorage.setItem('auth_user', JSON.stringify(user))
    localStorage.setItem('theme', 'light')
  }, USER)
  await page.route('**/setup/status', (route) => route.fulfill({ json: ok({ needs_setup: false, step: '' }) }))
  await page.route('**/api/v1/auth/me', (route) => route.fulfill({ json: ok(USER) }))
  await page.route('**/api/v1/settings/public', (route) => route.fulfill({
    json: ok({
      api_base_url: new URL(route.request().url()).origin,
      site_name: 'TokenKey',
      pricing_catalog_public: true,
      custom_menu_items: [],
      custom_endpoints: [],
    }),
  }))
  await page.route('**/api/v1/subscriptions/active', (route) => route.fulfill({ json: ok([]) }))
  await page.route('**/api/v1/announcements**', (route) => route.fulfill({ json: ok([]) }))
  await page.route('**/api/v1/public/pricing**', (route) => route.fulfill({
    json: {
      object: 'list',
      data: catalog(true).models.map((row) => ({
        model_id: row.model_id,
        vendor: row.vendor,
        capabilities: row.capabilities,
        pricing: { currency: 'USD', input_per_1k_tokens: 0.005, output_per_1k_tokens: 0.025 },
      })),
      updated_at: '2026-09-10T00:00:00Z',
    },
  }))
  await page.route('**/api/v1/me/pricing-catalog**', (route) => {
    const url = new URL(route.request().url())
    requests.push(url.search)
    return route.fulfill({ json: ok(catalog(url.searchParams.get('api_key_id') === '43')) })
  })
}

async function expectLayoutWithinViewport(page: Page): Promise<void> {
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  for (const selector of ['[data-tk="pricing-filter-key"]', '[data-tk="pricing-filter-group"]', '#pricing-model-search']) {
    const box = await page.locator(selector).boundingBox()
    expect(box).not.toBeNull()
    expect(box!.x).toBeGreaterThanOrEqual(0)
    expect(box!.x + box!.width).toBeLessThanOrEqual(page.viewportSize()!.width)
  }
}

for (const viewport of [{ width: 1280, height: 820 }, { width: 390, height: 844 }]) {
  test(`US050 candidate pricing scope at ${viewport.width}px`, async ({ page }, testInfo) => {
    await page.setViewportSize(viewport)
    const requests: string[] = []
    await installFixture(page, requests)
    await page.goto('/models?view=pricing')

    const table = page.locator('[data-tk="cold-start-pricing-table"]')
    const modelRow = (id: string) => table.locator('tbody tr').filter({ has: page.getByText(id, { exact: true }) })
    const keySelect = page.locator('[data-tk="pricing-filter-key"]')
    const groupSelect = page.locator('[data-tk="pricing-filter-group"]')
    await expect(table).toBeVisible({ timeout: 30_000 })
    await expect(keySelect).toHaveValue('42')
    await expect(groupSelect).toHaveValue('group:11')
    await expect(modelRow(CROSS_PLATFORM_MODEL)).toHaveCount(1)
    await expect(modelRow(ALIAS)).toHaveCount(1)
    await expect(modelRow(CROSS_PLATFORM_MODEL).locator('[data-tk="pricing-col-authorized-groups"]')).toContainText(GROUP.name)
    await expect(modelRow(RETIRED_MODEL)).toHaveCount(0)
    await expect(modelRow(UNIVERSAL_ONLY_MODEL)).toHaveCount(0)
    await expectLayoutWithinViewport(page)
    await page.screenshot({ path: testInfo.outputPath('direct-candidate-catalog.png'), fullPage: true })

    await keySelect.selectOption('43')
    await expect(modelRow(UNIVERSAL_ONLY_MODEL)).toHaveCount(1)
    await expect(groupSelect).toHaveValue('automatic')
    expect(requests.at(-1)).toBe('?api_key_id=43')
    await expect(modelRow(CROSS_PLATFORM_MODEL)).toHaveCount(1)
    await expect(modelRow(ALIAS)).toHaveCount(1)
    await expect(modelRow(RETIRED_MODEL)).toHaveCount(0)
    await expectLayoutWithinViewport(page)
    await page.screenshot({ path: testInfo.outputPath('universal-candidate-catalog.png'), fullPage: true })
    const groupBadge = modelRow(CROSS_PLATFORM_MODEL).getByText(GROUP.name, { exact: true })
    await groupBadge.scrollIntoViewIfNeeded()
    await expect(groupBadge).toBeInViewport()
    if (viewport.width < 640) {
      await expect.poll(() => groupBadge.evaluate((element) => {
        const box = element.getBoundingClientRect()
        return [box.left + 1, box.right - 1].every((x) => {
          const topmost = document.elementFromPoint(x, box.top + box.height / 2)
          return topmost === element || (topmost !== null && element.contains(topmost))
        })
      })).toBe(true)
    } else {
      await expect(modelRow(CROSS_PLATFORM_MODEL).locator('td').first()).toHaveCSS('position', 'sticky')
    }
    await expectLayoutWithinViewport(page)
    await page.screenshot({ path: testInfo.outputPath('candidate-authorized-groups.png'), fullPage: true })

    await groupSelect.selectOption('group:11')
    await expect(keySelect).toHaveValue('0')
    await expect(modelRow(UNIVERSAL_ONLY_MODEL)).toHaveCount(0)
    expect(requests.at(-1)).toBe('?group_id=11')

    await keySelect.selectOption('42')
    await expect(keySelect).toHaveValue('42')
    await expect.poll(() => requests.at(-1)).toBe('?api_key_id=42')
    await expect(modelRow(CROSS_PLATFORM_MODEL)).toHaveCount(1)
    await expect(modelRow(ALIAS)).toHaveCount(1)
    await expect(modelRow(RETIRED_MODEL)).toHaveCount(0)
    await expectLayoutWithinViewport(page)
  })
}
