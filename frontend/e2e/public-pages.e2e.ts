import { expect, test, type Page } from '@playwright/test'

const settings = {
  site_name: 'TokenKey', site_logo: '/logo.png', registration_enabled: true,
  signup_bonus_enabled: true, signup_bonus_balance_usd: 2,
  pricing_catalog_public: true, backend_mode_enabled: false,
  registration_offer: { state: 'open', signup_bonus_usd: '2.00' },
  login_agreement_documents: [], custom_menu_items: [], custom_endpoints: [],
}
// Synthetic models isolate navigation semantics from the evolving live catalog.
const catalog = {
  object: 'list', updated_at: '2026-09-11T00:00:00Z',
  data: ['model-a', 'model-b'].map(model_id => ({
    model_id, vendor: 'openai', capabilities: [], context_window: 128000,
    pricing: { currency: 'USD', input_per_1k_tokens: 0.001, output_per_1k_tokens: 0.002 },
  })),
}
const ok = (data: unknown) => ({ code: 0, message: 'ok', data })

test.beforeEach(async ({ context }) => {
  await context.addInitScript(config => { window.__APP_CONFIG__ = config as typeof window.__APP_CONFIG__ }, settings)
  await context.route('**/*', async route => {
    const url = new URL(route.request().url())
    if (url.hostname !== '127.0.0.1') return route.abort()
    // Fixed request latency makes sequential startup dependencies observable
    // without depending on a production server or a particular internet link.
    await new Promise(resolve => setTimeout(resolve, 80))
    if (url.pathname === '/api/v1/auth/refresh') {
      return route.fulfill({ status: 400, json: { code: 400, message: 'invalid request' } })
    }
    if (url.pathname === '/api/v1/settings/public') return route.fulfill({ json: ok(settings) })
    if (url.pathname === '/setup/status') return route.fulfill({ json: ok({ needs_setup: false }) })
    if (url.pathname === '/api/v1/public/pricing') return route.fulfill({ json: ok(catalog) })
    if (url.pathname.startsWith('/api/')) throw new Error(`Unexpected API: ${route.request().method()} ${url.pathname}`)
    return route.continue()
  })
})

async function openPricing(page: Page) {
  await page.goto('/models?view=pricing')
  await expect(page.locator('tbody tr')).toHaveCount(catalog.data.length)
}

test('a model card reapplies exact search to an already cached price table', async ({ page }) => {
  await openPricing(page)
  await page.getByRole('tab', { name: '浏览', exact: true }).click()
  await page.getByPlaceholder('搜索模型...').fill('model-b')
  await page.locator('a[href*="model=model-b"]').click()
  await expect(page.getByPlaceholder('按模型名称搜索…')).toHaveValue('model-b')
  await expect(page.locator('tbody tr')).toHaveCount(1)
  await expect(page.locator('tbody tr')).toContainText('model-b')

  await page.goBack()
  await page.getByRole('tab', { name: '价格表', exact: true }).click()
  await expect(page.getByPlaceholder('按模型名称搜索…')).toHaveValue('')
  await expect(page.locator('tbody tr')).toHaveCount(catalog.data.length)
})

for (const path of ['/home', '/login', '/models', '/models?view=pricing']) {
  test(`guest ${path} renders without payment SDKs or admin chrome`, async ({ page }, testInfo) => {
    const requested: string[] = []
    page.on('request', request => requested.push(request.url()))
    await page.goto(path)
    if (path.startsWith('/models')) {
      if (path.includes('pricing')) await expect(page.locator('tbody tr')).toHaveCount(catalog.data.length)
      else await expect(page.locator('a[href*="&model="]')).toHaveCount(catalog.data.length)
    } else await expect(page.locator('h1').first()).toBeVisible()
    await page.waitForTimeout(500)
    const metrics = await page.evaluate(() => ({
      fcp: performance.getEntriesByName('first-contentful-paint')[0]?.startTime,
      resources: performance.getEntriesByType('resource').map(entry => {
        const resource = entry as PerformanceResourceTiming
        return { path: new URL(resource.name).pathname, bytes: resource.transferSize }
      }),
    }))
    await testInfo.attach('public-startup', { body: JSON.stringify({ path, metrics, requested }), contentType: 'application/json' })
    console.log(JSON.stringify({ path, fcp: metrics.fcp, requests: requested.length, bytes: metrics.resources.reduce((n, r) => n + r.bytes, 0) }))
    expect(requested.filter(url => /(?:admin-shell-|AdminComplianceDialog-|AnnouncementPopup-|vendor-airwallex-|vendor-stripe-|airwallex\.com|js\.stripe\.com)/.test(url))).toEqual([])
  })
}

test('home and pricing show the same configured signup credit', async ({ page }) => {
  await page.goto('/home')
  const offer = page.locator('[data-tk="registration-action"]').first()
  await expect(offer).toBeVisible()
  const homeOffer = await offer.getByRole('status').innerText()
  expect(homeOffer).toContain(settings.registration_offer.signup_bonus_usd)
  await expect(page.locator('body')).not.toContainText('100 万')
  await page.getByRole('link', { name: '模型市场', exact: true }).first().click()
  await page.getByRole('tab', { name: '价格表', exact: true }).click()
  await expect(page.locator('[data-tk="registration-action"]').first().getByRole('status')).toHaveText(homeOffer)
})

test('a disabled signup bonus does not advertise free credit', async ({ page }) => {
  await page.addInitScript(config => { window.__APP_CONFIG__ = config as typeof window.__APP_CONFIG__ }, { ...settings, signup_bonus_enabled: false, registration_offer: { state: 'closed', signup_bonus_usd: '0' } })
  await page.goto('/home')
  await expect(page.locator('h1').first()).toBeVisible()
  await expect(page.locator('body')).not.toContainText('100 万')
})

for (const role of ['user', 'admin']) {
  test(`${role} can still load console chrome on the shared model route`, async ({ page, context }) => {
    const user = { id: 7, role, email: 'local@example.test', username: 'local', balance: 2,
      status: 'active', onboarding_tour_seen_at: '2026-09-01T00:00:00Z' }
    await context.addInitScript(account => {
      localStorage.setItem('auth_token', 'local-fixture-token')
      localStorage.setItem('auth_user', JSON.stringify(account))
    }, user)
    await context.route('**/api/v1/**', async route => {
      const path = new URL(route.request().url()).pathname
      if (path === '/api/v1/auth/me') return route.fulfill({ json: ok(user) })
      if (path === '/api/v1/announcements' || path === '/api/v1/subscriptions/active') return route.fulfill({ json: ok([]) })
      if (path === '/api/v1/admin/compliance') return route.fulfill({ json: ok({ required: false }) })
      if (path === '/api/v1/admin/settings') return route.fulfill({ json: ok(settings) })
      if (path === '/api/v1/admin/payment/config') return route.fulfill({ json: ok({ enabled: false }) })
      if (path === '/api/v1/admin/system/check-updates') return route.fulfill({ json: ok({
        current_version: '1.8.220', latest_version: '1.8.220', has_update: false, cached: true, build_type: 'release',
      }) })
      if (path === '/api/v1/keys') return route.fulfill({ json: ok({ items: [], total: 0 }) })
      return route.fallback()
    })
    const errors: string[] = []
    page.on('pageerror', error => errors.push(error.message))
    await page.goto('/models')
    await expect(page.locator('aside').first()).toBeVisible()
    await expect(page.locator(`aside a[href="${role === 'admin' ? '/admin/users' : '/keys'}"]`)).toBeVisible()
    await expect(page.locator('[data-tk^="models-marketplace-card-"]')).toHaveCount(catalog.data.length)
    expect(errors).toEqual([])
  })
}
