import { expect, test, type Page } from '@playwright/test'

// Real application routes and controls, with deterministic API responses.
// These tests require only the frontend dev server, never production credentials.
async function prepare(page: Page, tablePageSize?: number) {
  const user = { id: 1, email: 'admin@example.test', username: 'Admin', role: 'admin', status: 'active', balance: 0, concurrency: 10, run_mode: 'standard', onboarding_tour_seen_at: '2026-09-10T00:00:00Z' }
  const settings = { site_name: 'TokenKey', channel_monitor_enabled: true, channel_monitor_mode: 'v2', registration_enabled: false, email_login_enabled: true, password_login_enabled: true, table_default_page_size: tablePageSize ?? 20, table_page_size_options: [20, 200] }
  let config = { enabled: true, refresh_interval_seconds: 60, platforms: [], group_ids: [], ignored_error_categories: [], health_thresholds: {} }
  let saves = 0
  const errors: string[] = []
  page.on('pageerror', error => errors.push(error.message))
  await page.addInitScript(({ settings, tablePageSize }) => {
    if (tablePageSize) Object.assign(window, { __APP_CONFIG__: settings })
    localStorage.setItem('locale', 'en')
    localStorage.setItem('admin_guide_1_admin_v4_interactive', 'true')
    localStorage.setItem('group-hidden-columns', JSON.stringify(['capacity', 'account_count', 'is_exclusive', 'rate_multiplier', 'status']))
    localStorage.setItem('group-column-settings-version', '2')
  }, { settings, tablePageSize })
  await page.route('**/api/v1/**', async route => {
    const url = new URL(route.request().url())
    const path = url.pathname.replace('/api/v1', '')
    let data: unknown = {}
    if (path === '/settings/public') {
      data = settings
    } else if (path === '/auth/login') {
      data = { access_token: 'local-ui-test', expires_in: 3600, token_type: 'Bearer', user }
    } else if (path === '/auth/me') {
      data = user
    } else if (path === '/admin/channel-monitor-v2/config') {
      if (route.request().method() === 'PUT') {
        config = route.request().postDataJSON()
        saves++
      }
      data = config
    } else if (path === '/admin/groups') {
      const number = Number(url.searchParams.get('page') || 1)
      const size = Number(url.searchParams.get('page_size') || 20)
      data = {
        items: Array.from({ length: size }, (_, index) => ({
          id: (number - 1) * size + index + 1, name: `Page ${number} Group ${index + 1}`,
          platform: 'anthropic', status: 'active', subscription_type: 'standard', rate_multiplier: 1,
          sort_order: index, account_count: 0, supported_model_scopes: [],
        })), total: 600, page: number, page_size: size, pages: Math.ceil(600 / size),
      }
    } else if (path === '/admin/groups/usage-summary') {
      data = { retained_days: 7, groups: Array.from({ length: 600 }, (_, index) => ({ group_id: index + 1, today_cost: 1.25, yesterday_cost: 2.5, total_cost: 9.75 })) }
    } else if (path === '/admin/groups/live-capability') {
      data = { supported: false }
    } else if (path.startsWith('/admin/groups/')) {
      data = []
    } else if (path === '/admin/channel-monitors') {
      data = { items: [], total: 0, page: 1, page_size: 20 }
    }
    await route.fulfill({ json: { code: 0, message: 'success', data } })
  })
  await page.route('**/setup/status', route => route.fulfill({ json: { needs_setup: false } }))
  await page.goto('/login')
  await page.locator('input[type=email]').fill(user.email)
  await page.locator('input[type=password]').fill('LocalUITest123!')
  await page.locator('button[type=submit]').click()
  await page.waitForURL(url => !url.pathname.includes('/login'))
  return { saves: () => saves, errors }
}

for (const viewport of [{ width: 1360, height: 900 }, { width: 390, height: 844 }]) {
  test(`monitor settings save and reload at ${viewport.width}px`, async ({ page }) => {
    await page.setViewportSize(viewport)
    const state = await prepare(page)
    await page.goto('/admin/channels/monitor')
    const save = page.getByRole('button', { name: 'Save', exact: true })
    await expect(save).toBeDisabled()
    await page.getByRole('button', { name: '5 min', exact: true }).click()
    await expect(save).toBeEnabled()
    await save.click()
    await expect(save).toBeDisabled()
    expect(state.saves()).toBe(1)
    await page.reload()
    await expect(page.getByRole('button', { name: '5 min', exact: true })).toHaveClass(/tab-active/)
    await page.getByRole('tab', { name: /V1 history/ }).click()
    await expect(page.getByRole('button', { name: /Create Monitor/ }).first()).toBeVisible()
    await page.getByRole('tab', { name: 'V2 data monitor config' }).click()
    await expect(page.getByRole('button', { name: '5 min', exact: true })).toHaveClass(/tab-active/)
    await page.screenshot({ path: `e2e/artifacts/maintenance-monitor-${viewport.width}.png`, fullPage: true })
    expect(state.errors).toEqual([])
  })
}

test('virtualized groups remain measured and usable across pages', async ({ page }) => {
  await page.setViewportSize({ width: 1360, height: 900 })
  const state = await prepare(page, 200)
  await page.goto('/admin/groups')
  const table = page.locator('.table-wrapper').first()
  await expect(page.getByText('Page 1 Group 1', { exact: true })).toBeVisible()
  await expect(table.getByText('7-day total', { exact: true }).first()).toBeVisible()
  await expect(table.getByText('$9.75', { exact: true }).first()).toBeVisible()
  await expect.poll(() => table.locator('tbody tr[data-index]').count()).toBeLessThan(200)
  for (const number of [2, 3]) {
    await table.hover()
    await page.mouse.wheel(0, 600)
    await expect.poll(() => table.evaluate(element => element.scrollTop)).toBeGreaterThan(0)
    await page.getByRole('button', { name: `Go to page ${number}`, exact: true }).click()
    await expect(page.getByText(new RegExp(`^Page ${number} Group `)).first()).toBeVisible()
    await expect(page.getByText(new RegExp(`^Page ${number - 1} Group `))).toHaveCount(0)
    const rows = await table.locator('tbody tr[data-index]').evaluateAll(elements => elements.map(element => {
      const rect = element.getBoundingClientRect()
      return { top: rect.top, bottom: rect.bottom, height: rect.height }
    }))
    expect(rows.length).toBeGreaterThan(0)
    expect(rows.every(row => row.height > 0)).toBe(true)
    expect(rows.every((row, index) => index === 0 || row.top >= rows[index - 1].bottom - 1)).toBe(true)
  }
  await page.screenshot({ path: 'e2e/artifacts/maintenance-groups-desktop.png' })
  expect(state.errors).toEqual([])
})
