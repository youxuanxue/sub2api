import { expect, test } from '@playwright/test'

// Fixture-backed real UI: no generation request or paid upstream is needed.
test('versioned Grok video stays selectable and priced after switching tabs', async ({ page }, testInfo) => {
  const model = 'grok-imagine-video-1.5'
  const user = { id: 7, username: 'studio-regression', email: 'studio@tokenkey.test', role: 'user', balance: 100, status: 'active', onboarding_tour_seen_at: '2026-09-18T00:00:00Z' }
  const group = { id: 11, name: 'Video', platform: 'grok', rate_multiplier: 1, status: 'active' }
  const ok = (data: unknown) => ({ code: 0, message: 'success', data })
  await page.addInitScript((user) => {
    localStorage.setItem('auth_token', 'studio-fixture-token')
    localStorage.setItem('auth_user', JSON.stringify(user))
  }, user)
  await page.route('**/setup/status', (route) => route.fulfill({ json: ok({ needs_setup: false }) }))
  await page.route('**/api/v1/**', (route) => {
    const path = new URL(route.request().url()).pathname
    if (path === '/api/v1/auth/me') return route.fulfill({ json: ok(user) })
    if (path === '/api/v1/settings/public') return route.fulfill({ json: ok({ site_name: 'TokenKey', api_base_url: new URL(route.request().url()).origin, custom_menu_items: [], custom_endpoints: [] }) })
    if (path === '/api/v1/keys') return route.fulfill({ json: ok({ items: [{ id: 42, name: 'Video fixture', key: 'sk-fixture', status: 'active', routing_mode: 'direct', group_id: 11, group }], total: 1, page: 1, page_size: 50 }) })
    if (path === '/api/v1/me/pricing-catalog') return route.fulfill({ json: ok({ models: [{ model_id: model, vendor: 'xai', billing_mode: 'video', your_price: { currency: 'USD', per_second: 0.08 }, capabilities: ['video'] }] }) })
    if (path === '/api/v1/public/pricing') return route.fulfill({ json: { object: 'list', data: [{ model_id: model, vendor: 'xai', pricing: { currency: 'USD', billing_mode: 'video', output_cost_per_second: 0.08 } }] } })
    return route.fulfill({ json: ok([]) })
  })
  await page.route('**/v1/models', (route) => route.fulfill({ json: { object: 'list', data: [{ id: model }] } }))
  await page.goto('/studio?mode=video')
  const tile = page.getByTestId('studio-video-model').filter({ hasText: model })
  await expect(tile).toBeVisible()
  await tile.click()
  await expect(page.getByTestId('studio-video-generate')).toContainText('$')
  await expect(page.getByTestId('studio-video-generate')).toBeEnabled()
  await page.getByTestId('studio-mode-chat').click()
  await expect(page.getByTestId('studio-video-model')).toHaveCount(0)
  await page.getByTestId('studio-mode-video').click()
  await expect(tile).toBeVisible()
  await expect(page.getByTestId('studio-video-generate')).toContainText('$')
  await page.screenshot({ path: testInfo.outputPath('versioned-grok-video.png'), fullPage: true })
})
