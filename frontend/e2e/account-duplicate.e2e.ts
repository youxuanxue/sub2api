import { expect, test } from '@playwright/test'

for (const width of [1440, 390]) {
  test(`batch copy and authorize new account at ${width}px`, async ({ page }) => {
    test.setTimeout(60_000)
    await page.setViewportSize({ width, height: 1000 })
    const user = { id: 1, role: 'admin', email: 'operator@example.test', balance: 0, status: 'active' }
    const source = {
      id: 24, name: 'oauth-9', platform: 'openai', type: 'oauth', proxy_id: 7,
      concurrency: 1, priority: 1, status: 'active', schedulable: true,
      credentials: {}, group_ids: [], supported_protocols: [],
      created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z'
    }
    const accounts = [source]
    const requests: { count: number; key: string | undefined }[] = []
    let failed = false
    let authorizedId = ''
    await page.addInitScript(user => {
      localStorage.setItem('auth_token', 'fixture-token')
      localStorage.setItem('auth_user', JSON.stringify(user))
      localStorage.setItem('locale', 'en')
      localStorage.setItem('admin_guide_1_admin_v4_interactive', 'true')
    }, user)
    await page.route('https://**', route => route.abort())
    await page.route('**/setup/status', route => route.fulfill({ json: { code: 0, data: { needs_setup: false } } }))
    await page.route('**/api/v1/**', async route => {
      const path = new URL(route.request().url()).pathname
      let data: unknown = {}
      if (path === '/api/v1/auth/me') data = user
      else if (path === '/api/v1/admin/accounts') data = { items: accounts, total: accounts.length, page: 1, page_size: 20, pages: 1 }
      else if (path.endsWith('/duplicate')) {
        const count = route.request().postDataJSON()?.count ?? 1
        requests.push({ count, key: route.request().headers()['idempotency-key'] })
        if (!failed) {
          failed = true
          return route.fulfill({ status: 500, json: { code: 500, message: 'fixture retry needed' } })
        }
        const copies = Array.from({ length: count }, (_, i) => ({ ...source, id: 25 + i, name: `oauth-${10 + i}`, schedulable: false }))
        accounts.push(...copies)
        data = copies
      } else if (path.includes('/openai/') && path.endsWith('/generate-auth-url')) {
        data = { auth_url: 'https://auth.openai.com/oauth/authorize?state=fixture', session_id: 'session-fixture' }
      } else if (path.includes('/openai/') && path.endsWith('/exchange-code')) {
        data = { access_token: 'fixture-access', refresh_token: 'fixture-refresh', expires_in: 3600 }
      } else if (path.endsWith('/apply-oauth-credentials')) {
        authorizedId = path.split('/').at(-2)!
        data = accounts.find(a => String(a.id) === authorizedId)
      } else if (/\/admin\/accounts\/\d+$/.test(path)) data = accounts.find(a => String(a.id) === path.split('/').at(-1))
      else if (path.endsWith('/all')) data = []
      else if (path.includes('/announcements')) data = { items: [], total: 0 }
      await route.fulfill({ json: { code: 0, data } })
    })
    await page.goto('/admin/accounts')
    const accountRows = page.locator(width < 768 ? 'div.rounded-lg.border.p-4' : 'tr')
    const sourceRow = accountRows.filter({ hasText: source.name })
    await sourceRow.getByTestId('account-more-btn').click()
    await page.getByRole('button', { name: 'Duplicate Account', exact: true }).click()
    const dialog = page.getByRole('dialog')
    const count = dialog.getByTestId('duplicate-account-count')
    await expect(count).toHaveValue('1')
    await count.fill('0')
    await expect(dialog.getByRole('button', { name: 'Duplicate Account', exact: true })).toBeDisabled()
    expect(requests).toEqual([])
    await count.fill('3')
    await dialog.getByRole('button', { name: 'Duplicate Account', exact: true }).click()
    await expect(dialog.getByRole('alert')).toContainText('fixture retry needed')
    await dialog.getByRole('button', { name: 'Duplicate Account', exact: true }).click()
    await expect(dialog).toBeHidden()
    expect(requests).toHaveLength(2)
    expect(requests[1]).toEqual(requests[0])
    const copyRow = accountRows.filter({ hasText: 'oauth-12' })
    await expect(copyRow).toBeVisible()
    await copyRow.getByTestId('oauth-authorization-link').click()
    await expect(dialog).toContainText('oauth-12')
    await expect(dialog.locator('input[readonly]')).toHaveValue('https://auth.openai.com/oauth/authorize?state=fixture')
    await page.screenshot({ path: `e2e/artifacts/account-copy-${width}.png` })
    await dialog.locator('textarea').fill('http://localhost:1455/auth/callback?code=fixture-code&state=fixture')
    await dialog.getByRole('button', { name: 'Complete Authorization', exact: true }).click()
    await expect(dialog).toBeHidden()
    expect(authorizedId).toBe('27')
  })
}
