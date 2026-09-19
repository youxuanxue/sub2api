import { expect, test } from '@playwright/test'

// Local fixtures only: no browser navigation or requests to Google.
for (const width of [1440, 390]) {
  test(`account verification and OAuth recovery at ${width}px`, async ({ page, context }) => {
    await page.setViewportSize({ width, height: 1000 })
    await context.grantPermissions(['clipboard-read', 'clipboard-write'])
    const user = { id: 1, role: 'admin', email: 'operator@example.test', balance: 0, status: 'active' }
    const validationURL = 'https://accounts.google.com/verify?token=fixture&continue=https%3A%2F%2Fgoogle.com'
    const account = {
      id: 24, name: 'anti-fixture-24', platform: 'antigravity', type: 'oauth', proxy_id: 7,
      concurrency: 1, priority: 1, status: 'active', schedulable: true, error_message: null,
      credentials: { email: 'account@example.test' }, group_ids: [], supported_protocols: [],
      created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z',
      temp_unschedulable_until: '2099-01-01T00:00:00Z',
      temp_unschedulable_reason: `Antigravity validation required temporary cooldown: verify | validation_url: ${validationURL}`,
    }
    let starts = 0
    let exchanged: unknown
    let saved: unknown
    let savedPath = ''
    const oauth = { auth_url: 'https://accounts.google.com/o/oauth2/v2/auth?state=fixture', session_id: 'session-fixture', state: 'fixture', expires_at: Math.floor(Date.now() / 1000) + 1800 }
    const renewedOAuth = { ...oauth, auth_url: 'https://accounts.google.com/o/oauth2/v2/auth?state=renewed', session_id: 'session-renewed', state: 'renewed' }
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
      else if (path === '/api/v1/admin/accounts') data = { items: [account], total: 1, page: 1, page_size: 20, pages: 1 }
      else if (path.endsWith('/antigravity/oauth/auth-url')) {
        expect(route.request().postDataJSON()).toEqual({ proxy_id: 7 })
        starts++
        if (starts === 1) return route.fulfill({ status: 500, json: { code: 500, message: 'fixture generation failed' } })
        data = starts > 2 ? renewedOAuth : oauth
      } else if (path.endsWith('/antigravity/oauth/exchange-code')) {
        exchanged = route.request().postDataJSON()
        data = { access_token: 'fake-access', refresh_token: 'fake-refresh', expires_at: 1900000000, project_id: 'fixture-project' }
      } else if (path.endsWith('/apply-oauth-credentials')) {
        savedPath = path
        saved = route.request().postDataJSON()
        data = { ...account, temp_unschedulable_reason: null, temp_unschedulable_until: null }
      } else if (path.endsWith('/all')) data = []
      else if (path.includes('/announcements')) data = { items: [], total: 0 }
      await route.fulfill({ json: { code: 0, data } })
    })
    await page.goto('/admin/accounts')
    const verification = page.getByTestId('google-verification-link').first()
    await expect(verification.getByRole('link')).toHaveAttribute('href', validationURL)
    await verification.getByRole('button', { name: 'Copy Link', exact: true }).click()
    await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(validationURL)
    await page.getByTestId('antigravity-authorization-link').first().click()
    const dialog = page.getByRole('dialog')
    await expect(dialog.getByText(account.name, { exact: true })).toBeVisible()
    await expect(dialog.getByText('fixture generation failed')).toBeVisible()
    await expect(dialog.getByRole('button', { name: 'Complete Authorization', exact: true })).toBeDisabled()
    await dialog.getByRole('button', { name: 'Generate Auth URL', exact: true }).click()
    await expect(dialog.locator('input[readonly]')).toHaveValue(oauth.auth_url)
    await dialog.getByRole('button', { name: 'Copy URL', exact: true }).click()
    await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(oauth.auth_url)
    await expect(dialog.getByTestId('antigravity-browser-hint')).toContainText('Edge egress IP')
    await page.screenshot({ path: `e2e/artifacts/antigravity-recovery-${width}.png` })
    await dialog.locator('textarea').fill('http://localhost:8085/callback?state=fixture&code=fixture-code')
    await expect(dialog.locator('textarea')).toHaveValue('fixture-code')
    await dialog.getByRole('button', { name: 'Regenerate', exact: true }).click()
    await expect(dialog.locator('input[readonly]')).toHaveValue(renewedOAuth.auth_url)
    await expect(dialog.locator('textarea')).toHaveValue('')
    await expect(dialog.getByRole('button', { name: 'Complete Authorization', exact: true })).toBeDisabled()
    await dialog.locator('textarea').fill(width === 390 ? 'new-code' : 'http://localhost:8085/callback?state=renewed&code=new-code')
    await dialog.getByRole('button', { name: 'Complete Authorization', exact: true }).click()
    await expect(dialog).toBeHidden()
    expect(exchanged).toEqual({ session_id: 'session-renewed', state: 'renewed', code: 'new-code', proxy_id: 7 })
    expect(savedPath).toBe('/api/v1/admin/accounts/24/apply-oauth-credentials')
    expect(saved).toMatchObject({ type: 'oauth', credentials: { access_token: 'fake-access', refresh_token: 'fake-refresh', project_id: 'fixture-project' } })
    expect(starts).toBe(3)
  })
}
