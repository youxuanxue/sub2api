import { expect, test } from '@playwright/test'

for (const width of [1360, 390]) {
  test(`user status and public quota cards survive upstream merge at ${width}px`, async ({ page }) => {
    test.setTimeout(60_000)
    await page.setViewportSize({ width, height: 1000 })
    const admin = { id: 1, email: 'admin@example.test', role: 'admin', status: 'active', balance: 0, concurrency: 10, onboarding_tour_seen_at: '2026-09-23T00:00:00Z' }
    let account = { id: 42, email: 'review@example.test', username: 'Review user', role: 'user', status: 'active', balance: 10, concurrency: 5, current_concurrency: 3, allowed_groups: [], subscriptions: [], created_at: '2026-09-01T00:00:00Z', updated_at: '2026-09-01T00:00:00Z' }
    let updates = 0
    const errors: string[] = []
    page.on('pageerror', error => errors.push(error.message))
    await page.addInitScript(user => {
      localStorage.setItem('auth_token', 'fixture-token')
      localStorage.setItem('auth_user', JSON.stringify(user))
      localStorage.setItem('locale', 'en')
      localStorage.setItem('admin_guide_1_admin_v4_interactive', 'true')
    }, admin)
    await page.route('https://**', route => route.abort())
    await page.route('**/setup/status', route => route.fulfill({ json: { code: 0, data: { needs_setup: false } } }))
    await page.route('**/api/v1/**', async route => {
      const path = new URL(route.request().url()).pathname.replace('/api/v1', '')
      let data: unknown = {}
      if (path === '/auth/me') data = admin
      else if (path === '/settings/public') data = { site_name: 'TokenKey', run_mode: 'standard' }
      else if (path === '/admin/users') data = { items: [account], total: 1, page: 1, page_size: 20, pages: 1 }
      else if (path === '/admin/users/42' && route.request().method() === 'PUT') {
        account = { ...account, status: route.request().postDataJSON().status }
        updates++
        data = account
      } else if (path === '/usage/dashboard/stats') data = { total_actual_cost: 1, today_actual_cost: 1, by_platform: [{ platform: 'grok', total_requests: 1, total_tokens: 20, total_actual_cost: 1, today_actual_cost: 1 }] }
      else if (path === '/user/platform-quotas') data = { platform_quotas: [
        { platform: 'anthropic', daily_limit_usd: null, weekly_limit_usd: null, monthly_limit_usd: null },
        { platform: 'openai', daily_limit_usd: 10, daily_usage_usd: 2, weekly_limit_usd: null, monthly_limit_usd: null }
      ] }
      else if (path === '/admin/user-attributes' || path === '/announcements' || path.endsWith('/all') || path.includes('/subscriptions/active')) data = []
      else if (path.includes('/usage') || path.includes('/announcements')) data = { items: [], total: 0, stats: {}, trend: [], models: [] }
      await route.fulfill({ json: { code: 0, data } })
    })

    await page.goto('/admin/users')
    await page.getByRole('button', { name: 'Disable', exact: true }).click()
    await expect(page.getByRole('button', { name: 'Enable', exact: true })).toBeVisible()
    expect(updates).toBe(1)
    await page.screenshot({ path: `e2e/artifacts/upstream-user-status-${width}.png`, fullPage: true })

    await page.goto('/dashboard')
    await expect(page.getByTestId('platform-card')).toHaveCount(2)
    await expect(page.locator('[data-platform="openai"]')).toContainText('10.00')
    await expect(page.locator('[data-platform="grok"]')).toContainText('1.0000')
    await expect(page.locator('[data-platform="anthropic"]')).toHaveCount(0)
    await page.screenshot({ path: `e2e/artifacts/upstream-platform-quota-${width}.png`, fullPage: true })
    expect(errors).toEqual([])
  })
}
