import { expect, test } from '@playwright/test'

// Exercise the real Accounts view with deterministic upstream/local usage
// fixtures. No production credentials or upstream requests are needed.
for (const viewport of [{ width: 1440, height: 1000 }, { width: 390, height: 844 }]) {
  test(`account usage windows distinguish local stats and quotas at ${viewport.width}px`, async ({ page }) => {
    await page.setViewportSize(viewport)
    const user = { id: 1, role: 'admin', email: 'usage-test@example.test', balance: 0, status: 'active' }
    const reset = new Date(Date.now() + 3 * 86400000).toISOString()
    const stats = { requests: 123, tokens: 456000, cost: 7.89, user_cost: 1.23 }
    const accounts = ['ali-token-plan', 'qianfan-token-plan', 'volcengine-agent-plan'].map((name, index) => ({
      id: index + 1, name, platform: 'newapi', type: 'apikey', channel_type: [17, 46, 45][index],
      status: 'active', schedulable: index !== 0, concurrency: 100, priority: 1,
      credentials: {}, extra: {}, groups: [], group_ids: [], rate_multiplier: 1,
      created_at: '2026-09-01T00:00:00Z', updated_at: '2026-09-01T00:00:00Z',
      rate_limit_reset_at: index === 0 ? reset : null,
    }))
    const usage = Object.fromEntries(accounts.map(a => [a.id, {
      source: 'passive',
      five_hour: { utilization: 0, utilization_unknown: true, resets_at: null, remaining_seconds: 0, window_stats: stats },
      seven_day: { utilization: a.id === 2 ? 0 : 100, utilization_unknown: a.id === 2, resets_at: a.id === 2 ? null : reset, remaining_seconds: 259200, window_stats: stats },
      upstream_quota: { provider: 'newapi', state: a.id === 2 ? 'unknown' : 'degraded', dimensions: a.id === 2 ? [] : [{ key: 'newapi_weekly', label: 'Weekly', utilization: 100, window: '7d', resets_at: reset }] }
    }]))
    await page.addInitScript(({ user }) => {
      localStorage.setItem('auth_token', 'usage-fixture-token')
      localStorage.setItem('auth_user', JSON.stringify(user))
      localStorage.setItem('locale', 'en')
      localStorage.setItem('admin_guide_1_admin_v4_interactive', 'true')
      localStorage.setItem('account-column-settings-version', '3')
      localStorage.setItem('account-hidden-columns', JSON.stringify(['id', 'platform_type', 'capacity', 'status', 'schedulable', 'today_stats', 'groups', 'proxy', 'priority', 'scheduler_score', 'rate_multiplier', 'upstream_billing_rate', 'last_used_at', 'created_at', 'expires_at', 'notes']))
    }, { user })
    await page.route('**/api/v1/**', async route => {
      const path = new URL(route.request().url()).pathname
      let data: unknown = {}
      if (path === '/api/v1/auth/me') data = user
      else if (path === '/api/v1/admin/accounts') data = { items: accounts, total: accounts.length, page: 1, page_size: 20, pages: 1 }
      else if (path === '/api/v1/admin/accounts/usage/batch') data = { usage }
      else if (path === '/api/v1/admin/accounts/today-stats/batch') data = { stats: Object.fromEntries(accounts.map(a => [a.id, stats])) }
      else if (path.endsWith('/all')) data = []
      else if (path.includes('/announcements')) data = { items: [], total: 0 }
      await route.fulfill({ json: { code: 0, message: 'success', data } })
    })
    await page.goto('/admin/accounts')
    const accountRow = (name: string) => viewport.width < 768
      ? page.locator('[data-field="name"]').filter({ hasText: name }).locator('..')
      : page.getByRole('row').filter({ hasText: name })
    const ali = accountRow('ali-token-plan')
    const qianfan = accountRow('qianfan-token-plan')
    await expect(ali.getByTestId('usage-stats-row')).toHaveCount(3)
    await expect(ali).toContainText('Today')
    await expect(ali).toContainText('Last 5h')
    await expect(ali).toContainText('Last 7d')
    await expect(ali.getByTestId('usage-quota-row')).toHaveCount(1)
    await expect(ali.getByTestId('usage-quota-row')).toContainText('100%')
    await expect(qianfan.getByTestId('usage-stats-row')).toHaveCount(3)
    await expect(qianfan.getByTestId('usage-quota-row')).toHaveCount(0)
    const overflowing = await page.getByTestId('usage-stats-row').evaluateAll(rows => rows.filter(row => row.scrollWidth > row.clientWidth + 1).length)
    expect(overflowing).toBe(0)
    await page.screenshot({ path: `e2e/artifacts/account-usage-${viewport.width}.png`, fullPage: true })
  })
}
