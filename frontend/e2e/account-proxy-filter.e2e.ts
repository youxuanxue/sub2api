import { expect, test } from '@playwright/test'

for (const width of [1440, 390]) {
  test(`filter accounts by proxy name at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 })
    const user = { id: 1, role: 'admin', email: 'proxy-test@example.test', balance: 0, status: 'active' }
    const accounts = ['proxy-match', 'proxy-other', 'direct-account'].map((name, index) => ({
      id: index + 1, name, platform: 'openai', type: 'apikey', proxy_id: index < 2 ? index + 1 : null,
      concurrency: 1, priority: 1, status: 'active', schedulable: true,
      credentials: {}, extra: {}, group_ids: [], supported_protocols: [],
      created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z'
    }))
    const queries: string[] = []
    await page.addInitScript(user => {
      localStorage.setItem('auth_token', 'fixture-token')
      localStorage.setItem('auth_user', JSON.stringify(user))
      localStorage.setItem('locale', 'en')
      localStorage.setItem('admin_guide_1_admin_v4_interactive', 'true')
    }, user)
    await page.route('https://**', route => route.abort())
    await page.route('**/setup/status', route => route.fulfill({ json: { code: 0, data: { needs_setup: false } } }))
    await page.route('**/api/v1/**', async route => {
      const url = new URL(route.request().url())
      let data: unknown = {}
      if (url.pathname === '/api/v1/auth/me') data = user
      else if (url.pathname === '/api/v1/admin/accounts') {
        const query = url.searchParams.get('proxy_name') || ''
        queries.push(query)
        const items = query === 'Office proxy' ? accounts.slice(0, 1) : query ? [] : accounts
        data = { items, total: items.length, page: 1, page_size: 100, pages: 1 }
      } else if (url.pathname === '/api/v1/admin/proxies/all') data = [
        { id: 1, name: 'Office proxy', host: '192.0.2.10', port: 8080, protocol: 'http', status: 'active' },
        { id: 2, name: 'Unused proxy', host: '198.51.100.20', port: 8080, protocol: 'http', status: 'active' }
      ]
      else if (url.pathname.endsWith('/all')) data = []
      else if (url.pathname.includes('/announcements')) data = { items: [], total: 0 }
      await route.fulfill({ json: { code: 0, data } })
    })
    await page.goto('/admin/accounts')
    await expect(page.getByText('proxy-other', { exact: true })).toBeVisible()
    await page.getByRole('button', { name: /more filters/i }).click()
    const select = page.getByRole('button', { name: 'All Proxies', exact: true })
    await select.click()
    await page.getByRole('option', { name: 'Office proxy', exact: true }).click()
    await expect(page.getByText('proxy-other', { exact: true })).toHaveCount(0)
    await expect(page.getByText('direct-account', { exact: true })).toHaveCount(0)
    await expect(page.getByText('proxy-match', { exact: true })).toBeVisible()
    expect(queries.at(-1)).toBe('Office proxy')
    await page.screenshot({ path: `e2e/artifacts/account-proxy-filter-${width}.png`, fullPage: true })
    await select.click()
    await page.getByRole('option', { name: 'Unused proxy', exact: true }).click()
    await expect(page.getByText('proxy-match', { exact: true })).toHaveCount(0)
    expect(queries.at(-1)).toBe('Unused proxy')
    await select.click()
    await page.getByRole('option', { name: 'All Proxies', exact: true }).click()
    await expect(page.getByText('proxy-other', { exact: true })).toBeVisible()
    await expect(page.getByText('direct-account', { exact: true })).toBeVisible()
    expect(queries.at(-1)).toBe('')
  })
}
