import { expect, test } from '@playwright/test'

test('US054 administrator filters machine operations and inspects independent credential ID', async ({ page }) => {
  const admin = {
    id: 19, email: 'machine-audit@tokenkey.test', role: 'admin', status: 'active',
    balance: 100, concurrency: 10, allowed_groups: [],
    onboarding_tour_seen_at: '2026-09-11T00:00:00Z',
  }
  const entry = {
    id: 54, actor_user_id: admin.id, actor_email: admin.email, actor_role: 'admin',
    created_at: '2026-09-11T08:00:00Z', auth_method: 'machine_admin_key',
    action: 'admin.accounts.export', method: 'GET', path: '/api/v1/admin/accounts/data',
    status_code: 200, latency_ms: 10, client_ip: '192.0.2.19',
    credential_masked: '', request_id: 'us054-fixture', user_agent: 'test-automation',
    extra: { machine_key_id: '0123456789abcdef0123456789abcdef' },
  }
  const filters: string[] = []
  await page.addInitScript((user) => {
    localStorage.setItem('auth_token', 'us054-fixture-session')
    localStorage.setItem('auth_user', JSON.stringify(user))
    localStorage.setItem('tokenkey_locale', 'en')
  }, admin)
  await page.route('**/setup/status', route => route.fulfill({ json: { code: 0, data: { needs_setup: false } } }))
  await page.route('**/api/v1/**', async route => {
    const url = new URL(route.request().url())
    let data: unknown = {}
    if (url.pathname.endsWith('/auth/me')) data = admin
    else if (url.pathname.endsWith('/settings/public')) data = { site_name: 'TokenKey', custom_menu_items: [], custom_endpoints: [] }
    else if (url.pathname.endsWith('/compliance')) data = { accepted: true }
    else if (url.pathname.endsWith('/audit-logs')) {
      filters.push(url.searchParams.get('auth_method') || '')
      data = { items: [entry], total: 1, page: 1, page_size: 20, pages: 1 }
    } else if (url.pathname.endsWith('/audit-logs/54')) data = entry
    else if (url.pathname.includes('/announcements') || url.pathname.includes('/subscriptions')) data = []
    await route.fulfill({ json: { code: 0, message: 'success', data } })
  })
  await page.goto('/admin/audit-logs')
  await page.getByTestId('audit-auth-method').getByRole('button').click()
  await page.getByRole('option', { name: 'Machine Admin Key', exact: true }).click()
  await expect.poll(() => filters.at(-1)).toBe('machine_admin_key')
  await expect(page.getByText('admin · Machine Admin Key', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: /detail/i }).first().click()
  await expect(page.getByText(/0123456789abcdef0123456789abcdef/)).toBeVisible()
  await page.screenshot({ animations: 'disabled', path: 'e2e/artifacts/us054-machine-audit.png' })
})
