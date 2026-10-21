import { expect, test } from '@playwright/test'

// Real Accounts view; local API fixtures isolate quota display from credentials.
test('Codex quota query replaces 100%, persists on reload, and reports failed refreshes', async ({ page }) => {
  const user = { id: 1, role: 'admin', email: 'quota@example.test', balance: 0, status: 'active' }
  const account = { id: 283, name: 'quota-refresh-fixture', platform: 'openai', type: 'oauth', status: 'active', schedulable: true,
    concurrency: 25, priority: 1, credentials: {}, extra: {}, groups: [], group_ids: [], rate_multiplier: 1,
    created_at: '2026-10-01T00:00:00Z', updated_at: '2026-10-21T10:00:00Z' }
  const reset = new Date(Date.now() + 6 * 86400000).toISOString()
  let percentage = 100
  let fail = false
  let activeQueries = 0
  const usage = () => ({ source: 'passive', updated_at: percentage === 100 ? '2026-10-21T10:00:00Z' : '2026-10-21T12:00:00Z', seven_day: { utilization: percentage, resets_at: reset, remaining_seconds: 518400 } })
  await page.addInitScript(({ user }) => {
    localStorage.setItem('auth_token', 'quota-fixture-token')
    localStorage.setItem('auth_user', JSON.stringify(user))
    localStorage.setItem('locale', 'en')
    localStorage.setItem('admin_guide_1_admin_v4_interactive', 'true')
    localStorage.setItem('account-column-settings-version', '3')
    localStorage.setItem('account-hidden-columns', JSON.stringify(['id', 'platform_type', 'capacity', 'schedulable', 'today_stats', 'groups', 'proxy', 'priority', 'scheduler_score', 'rate_multiplier', 'upstream_billing_rate', 'last_used_at', 'created_at', 'expires_at', 'notes']))
  }, { user })
  await page.route('**/api/v1/**', async route => {
    const url = new URL(route.request().url())
    const path = url.pathname
    let data: unknown = {}
    if (path === '/api/v1/auth/me') data = user
    else if (path === '/api/v1/admin/accounts') data = { items: [account], total: 1, page: 1, page_size: 20, pages: 1 }
    else if (path === '/api/v1/admin/accounts/usage/batch') data = { usage: { 283: usage() } }
    else if (path === '/api/v1/admin/accounts/283/usage') {
      if (url.searchParams.get('source') === 'active') {
        activeQueries++
        if (fail) {
          await route.fulfill({ status: 502, json: { code: 'OPENAI_QUOTA_UPSTREAM_ERROR', message: 'Quota unavailable' } })
          return
        }
        percentage = 1
      }
      data = usage()
    } else if (path === '/api/v1/admin/accounts/today-stats/batch') data = { stats: {} }
    else if (path.endsWith('/all')) data = []
    else if (path.includes('/announcements')) data = { items: [], total: 0 }
    await route.fulfill({ json: { code: 0, message: 'success', data } })
  })
  await page.goto('/admin/accounts')
  const row = page.getByRole('row').filter({ hasText: account.name })
  await expect(row.getByTestId('usage-quota-row')).toContainText('100%')
  expect(activeQueries).toBe(0)
  await row.getByRole('button', { name: 'Query', exact: true }).click()
  await expect(row.getByTestId('usage-quota-row')).toContainText('1%')
  expect(activeQueries).toBe(1)
  await page.reload()
  await expect(row.getByTestId('usage-quota-row')).toContainText('1%')
  expect(activeQueries).toBe(1)
  fail = true
  await row.getByRole('button', { name: 'Query', exact: true }).click()
  await expect(row.getByRole('alert')).toBeVisible()
  await expect(row.getByTestId('usage-quota-row')).toContainText('1%')
})
