import { expect, test } from '@playwright/test'

const backend = process.env.TK_CANDIDATE_BACKEND_URL

test.skip(!backend, 'Started by TestCandidatePricingBrowser with a real catalog service')

for (const width of [1280, 390]) {
  test(`US050 real candidate catalog at ${width}px`, async ({ page, request }, testInfo) => {
    const fixture = await (await request.get(`${backend}/fixture`)).json()
    const [directModel, alias, universalModel] = fixture.models as string[]
    const user = { id: fixture.user_id, username: 'candidate-test', email: 'candidate@test.invalid',
      role: 'user', status: 'active', balance: 100, concurrency: 5,
      onboarding_tour_seen_at: '2026-09-11T00:00:00Z' }
    await page.setViewportSize({ width, height: 844 })
    await page.addInitScript((value) => {
      localStorage.setItem('auth_token', 'isolated-test-session')
      localStorage.setItem('auth_user', JSON.stringify(value))
      localStorage.setItem('theme', 'light')
    }, user)
    const ok = (data: unknown) => ({ code: 0, data })
    await page.route('**/setup/status', route => route.fulfill({ json: ok({ needs_setup: false }) }))
    await page.route('**/api/v1/auth/me', route => route.fulfill({ json: ok(user) }))
    await page.route('**/api/v1/settings/public', route => route.fulfill({ json: ok({
      site_name: 'TokenKey', pricing_catalog_public: true, custom_menu_items: [], custom_endpoints: [],
    }) }))
    await page.route('**/api/v1/subscriptions/active', route => route.fulfill({ json: ok([]) }))
    await page.route('**/api/v1/announcements**', route => route.fulfill({ json: ok([]) }))
    await page.route('**/api/v1/public/pricing**', route => route.fulfill({ json: { object: 'list', data: [] } }))
    // Forward the request to the Go service; do not manufacture its response.
    let failureKind = ''
    let evidenceExpired = false
    await page.route('**/api/v1/me/pricing-catalog**', async route => {
      const url = new URL(route.request().url())
      const response = await route.fetch({ url: `${backend}${url.pathname}${url.search}`,
        headers: { ...route.request().headers(), 'X-Test-Failure-Kind': failureKind,
          'X-Test-Evidence-Expired': String(evidenceExpired) } })
      await route.fulfill({ response })
    })
    await page.goto('/models?view=pricing')
    const table = page.locator('[data-tk="cold-start-pricing-table"]')
    const row = (id: string) => table.locator('tbody tr').filter({ has: page.getByText(id, { exact: true }) })
    const keySelect = page.locator('[data-tk="pricing-filter-key"]')
    await expect(table).toBeVisible()
    await keySelect.selectOption('42')
    await expect(row(directModel)).toHaveCount(1)
    await expect(row(alias)).toHaveCount(1)
    await expect(row(universalModel)).toHaveCount(0)
    await expect(row(directModel)).toContainText('Cross-platform billing')
    await keySelect.selectOption('43')
    await expect(row(universalModel)).toHaveCount(1)
    await expect(row(directModel)).toHaveCount(1)
    for (const [kind, expired] of [
      ['rate_limited', false], ['auth_failure', false], ['upstream_5xx', false],
      ['model_not_found', false], ['provider_model_retired', false],
      ['provider_model_retired', true], ['', false],
    ] as const) {
      failureKind = kind
      evidenceExpired = expired
      const catalogLoaded = page.waitForResponse(response => response.url().includes('/api/v1/me/pricing-catalog'))
      await page.reload()
      expect((await catalogLoaded).ok()).toBe(true)
      await expect(keySelect).toBeVisible()
      await keySelect.selectOption('43')
      const expected = kind === 'provider_model_retired' && !expired ? 0 : 1
      if (expected === 0) {
        await expect(page.getByText('This group has no models yet', { exact: true })).toBeVisible()
      } else {
        await expect(table).toBeVisible()
      }
      await expect(row(directModel)).toHaveCount(expected)
      await expect(row(universalModel)).toHaveCount(expected)
    }
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
    await page.screenshot({ path: testInfo.outputPath('candidate-service-catalog.png'), fullPage: true })
  })
}
