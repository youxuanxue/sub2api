import { expect, test } from '@playwright/test'

for (const width of [1440, 390]) {
  test(`risk allowlist saves through the shared settings form at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 })
    const admin = { id: 1, role: 'admin', email: 'merge@tokenkey.test', balance: 10, status: 'active', onboarding_tour_seen_at: '2026-09-30T00:00:00Z' }
    await page.addInitScript(user => {
      localStorage.setItem('auth_token', 'merge-settings-test')
      localStorage.setItem('auth_user', JSON.stringify(user))
      localStorage.setItem('tokenkey_locale', 'en')
    }, admin)
    const errors: string[] = []
    page.on('pageerror', error => errors.push(error.message))
    const settings = {
      site_name: 'TokenKey', custom_menu_items: [], custom_endpoints: [], default_subscriptions: [],
      registration_email_suffix_whitelist: [], forwarded_client_ip_headers: [],
      table_default_page_size: 20, table_page_size_options: [10, 20, 50, 100],
      cyber_policy_user_allowlist: '', cyber_session_block_enabled: true, cyber_session_block_ttl_seconds: 300,
      openai_codex_client_version_synced: '', openai_fast_policy_settings: { rules: [] },
    }
    let saved: Record<string, unknown> | undefined
    await page.route('**/setup/status', route => route.fulfill({ json: { code: 0, data: { needs_setup: false } } }))
    await page.route('**/api/v1/**', async route => {
      const request = route.request()
      const requestPath = new URL(request.url()).pathname
      let data: unknown = {}
      if (requestPath === '/api/v1/auth/me') data = admin
      else if (requestPath === '/api/v1/settings/public') data = { site_name: 'TokenKey', custom_menu_items: [] }
      else if (requestPath === '/api/v1/admin/settings') {
        if (request.method() === 'PUT') saved = request.postDataJSON()
        data = { ...settings, ...saved }
      } else if (requestPath === '/api/v1/admin/usage/search-users') data = [{ id: 42, email: 'trusted@example.test' }]
      else if (requestPath === '/api/v1/admin/compliance') data = { required: false }
      else if (/\/(groups|proxies)\/all$|\/providers$/.test(requestPath)) data = []
      else if (/\/groups$|\/proxies$/.test(requestPath)) data = { items: [], total: 0 }
      else if (/\/active$|\/announcements$/.test(requestPath)) data = []
      await route.fulfill({ json: { code: 0, message: 'success', data } })
    })
    await page.goto('/admin/settings')
    await page.locator('#settings-tab-features').click()
    const allowlist = page.getByText('Risk control allowlist', { exact: true }).locator('..')
    await allowlist.locator('input').fill('trusted')
    await allowlist.getByRole('button', { name: /trusted@example.test/ }).click()
    await expect(allowlist).toContainText('trusted@example.test')
    await page.getByRole('button', { name: /^Save$/i }).click()
    await expect.poll(() => saved?.cyber_policy_user_allowlist).toBe('42')
    expect(saved?.cyber_session_block_enabled).toBe(true)
    await expect(allowlist).toContainText('trusted@example.test')
    await expect(page.locator('[aria-live="polite"] .border-red-500')).toHaveCount(0)
    expect(errors).toEqual([])
    await page.screenshot({ path: test.info().outputPath('risk-allowlist.png'), fullPage: true })
  })
}

for (const width of [1440, 390]) {
  test(`OpenAI editor keeps one TokenKey GPT-6.1 mapping preset at ${width}px`, async ({ page }) => {
    test.setTimeout(60_000)
    await page.setViewportSize({ width, height: 900 })
    const admin = { id: 1, role: 'admin', email: 'merge@tokenkey.test', balance: 10, status: 'active', onboarding_tour_seen_at: '2026-09-30T00:00:00Z' }
    const account = { id: 62, name: 'Merge OpenAI fixture', platform: 'openai', type: 'apikey', status: 'active', schedulable: true, concurrency: 1, priority: 1, group_ids: [], credentials: { base_url: 'https://provider.example.test', model_mapping: {} }, credentials_status: { has_api_key: true }, extra: {} }
    let saved: Record<string, unknown> | undefined
    const errors: string[] = []
    page.on('pageerror', error => errors.push(error.message))
    await page.addInitScript(user => {
      localStorage.setItem('auth_token', 'merge-preset-test')
      localStorage.setItem('auth_user', JSON.stringify(user))
      localStorage.setItem('tokenkey_locale', 'en')
      localStorage.setItem('locale', 'en')
      localStorage.setItem('admin_guide_1_admin_v4_interactive', 'true')
    }, admin)
    await page.route('https://**', route => route.abort())
    await page.route('**/setup/status', route => route.fulfill({ json: { code: 0, data: { needs_setup: false } } }))
    await page.route('**/api/v1/**', async route => {
      const request = route.request()
      const requestPath = new URL(request.url()).pathname
      let data: unknown = {}
      if (requestPath === '/api/v1/auth/me') data = admin
      else if (requestPath === '/api/v1/settings/public') data = { site_name: 'TokenKey', custom_menu_items: [] }
      else if (requestPath === '/api/v1/admin/accounts') data = { items: [account], total: 1, page: 1, page_size: 20, pages: 1 }
      else if (requestPath === '/api/v1/admin/accounts/62') {
        if (request.method() === 'PUT') saved = request.postDataJSON()
        data = { ...account, ...saved }
      } else if (requestPath.endsWith('/all') || requestPath.includes('/model-mapping-presets')) data = []
      else if (requestPath.includes('/announcements')) data = { items: [], total: 0 }
      await route.fulfill({ json: { code: 0, data } })
    })
    await page.goto('/admin/accounts')
    await page.locator('[data-testid="account-edit-btn"]:visible').click()
    const dialog = page.getByRole('dialog')
    await dialog.getByRole('button', { name: 'Model Mapping', exact: true }).click()
    const preset = dialog.getByRole('button', { name: '+ GPT-6.1 Sol', exact: true })
    await expect(preset).toHaveCount(1)
    await expect(preset).toHaveClass(/bg-rose-100/)
    await preset.click()
    await expect(dialog.getByPlaceholder('Request model')).toHaveValue('gpt-6.1-sol')
    await expect(dialog.getByPlaceholder('Actual model')).toHaveValue('gpt-6.1-sol')
    await page.screenshot({ path: test.info().outputPath('gpt61-preset.png'), fullPage: true })
    await dialog.getByRole('button', { name: 'Update', exact: true }).click()
    await expect.poll(() => (saved?.credentials as { model_mapping?: unknown } | undefined)?.model_mapping).toEqual({ 'gpt-6.1-sol': 'gpt-6.1-sol' })
    expect(errors).toEqual([])
  })
}
