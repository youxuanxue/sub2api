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
      const filtered = !!url.searchParams.get('search')
      data = {
        items: Array.from({ length: filtered ? 1 : size }, (_, index) => ({
          id: (number - 1) * size + index + 1, name: `Page ${number} Group ${index + 1}`,
          platform: 'anthropic', status: 'active', subscription_type: 'standard', rate_multiplier: 1,
          sort_order: index, account_count: 0, supported_model_scopes: [],
        })), total: filtered ? 1 : 600, page: number, page_size: size, pages: filtered ? 1 : Math.ceil(600 / size),
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

for (const viewport of [{ width: 1360, height: 900 }, { width: 1024, height: 720 }, { width: 1920, height: 1080 }]) {
test(`virtualized groups remain usable across pages and filters at ${viewport.width}px`, async ({ page }) => {
  await page.setViewportSize(viewport)
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
    await expect.poll(() => table.locator('tbody tr[data-index]').evaluateAll(elements => {
      const rows = elements.map(element => element.getBoundingClientRect())
      return rows.length > 0 && rows.every((row, index) =>
        row.height > 0 && (index === 0 || row.top >= rows[index - 1].bottom - 1))
    })).toBe(true)
  }
  const search = page.getByPlaceholder('Search groups...')
  await search.fill('Page 1 Group 1')
  await expect(table.locator('tbody tr[data-index]')).toHaveCount(1)
  await expect(table.locator('tbody tr[aria-hidden="true"]')).toHaveCount(0)
  await search.clear()
  await expect.poll(() => table.locator('tbody tr[data-index]').count()).toBeGreaterThan(1)
  await expect.poll(() => table.locator('tbody tr[data-index]').count()).toBeLessThan(200)
  await page.setViewportSize({ width: 390, height: 844 })
  await expect(page.getByText('Page 1 Group 1', { exact: true })).toBeVisible()
  await page.setViewportSize(viewport)
  await table.hover()
  await page.mouse.wheel(0, -30000)
  await expect(page.getByText('Page 1 Group 1', { exact: true })).toBeVisible()
  const pagination = page.getByRole('button', { name: 'Go to page 2', exact: true })
  await expect(pagination).toBeInViewport()
  await page.screenshot({ path: `e2e/artifacts/maintenance-groups-${viewport.width}.png` })
  expect(state.errors).toEqual([])
})
}

test('edge Grok queries remain isolated and refresh replaces queried usage', async ({ page }) => {
  await page.setViewportSize({ width: 1360, height: 900 })
  const state = await prepare(page)
  const queries: string[] = []
  let snapshots = 0
  const mainQueries: string[] = []
  page.on('request', request => {
    if (/\/admin\/(grok\/)?accounts\/99\//.test(request.url())) mainQueries.push(request.url())
  })
  await page.route('**/api/v1/admin/edge-accounts**', async route => {
    const path = new URL(route.request().url()).pathname
    if (path.endsWith('/usage')) {
      queries.push(path)
      if (path.includes('/edge-b/')) {
        await route.fulfill({ status: 502, json: { code: 502, message: 'edge unavailable' } })
        return
      }
      await route.fulfill({ json: { code: 0, data: { grok_billing: { prepaid_balance: 12.5 } } } })
      return
    }
    snapshots++
    await route.fulfill({ json: { code: 0, data: {
      platform: 'all', ts: snapshots,
      edges: ['edge-a', 'edge-b'].map(edge_id => ({
        edge_id, base_url: `https://${edge_id}.example.test`, ok: true, stub_schedulable: true,
        accounts: [{
          id: 99, name: `${edge_id} Grok`, platform: 'grok', type: 'oauth', status: 'active',
          schedulable: true, is_schedulable: true, concurrency: 1, priority: 1, rate_multiplier: 1,
          created_at: '2026-09-10T00:00:00Z',
          usage: { source: 'passive', updated_at: `2026-09-10T00:00:0${snapshots}Z`,
            upstream_quota: { provider: 'grok', state: 'observed', dimensions: [{ key: 'grok_tokens', label: 'Tokens', remaining: 300 - snapshots, limit: 500 }] },
          },
        }],
      })),
    } } })
  })
  await page.goto('/admin/edge-accounts')
  const first = page.getByRole('row').filter({ hasText: 'edge-a Grok' })
  const second = page.getByRole('row').filter({ hasText: 'edge-b Grok' })
  await expect(first).toBeVisible()
  expect(queries).toEqual([])
  await first.getByRole('button', { name: 'Query', exact: true }).click()
  await expect(first).toContainText('$12.50')
  await second.getByRole('button', { name: 'Query', exact: true }).click()
  await expect(second).toContainText('edge unavailable')
  await expect(second).not.toContainText('$12.50')
  await page.getByRole('button', { name: 'Refresh', exact: true }).click()
  await expect.poll(() => snapshots).toBe(2)
  await expect(first).toContainText('298/500')
  await expect(first).not.toContainText('$12.50')
  expect(queries).toEqual([
    '/api/v1/admin/edge-accounts/edge-a/accounts/99/usage',
    '/api/v1/admin/edge-accounts/edge-b/accounts/99/usage',
  ])
  expect(mainQueries).toEqual([])
  expect(state.errors).toEqual([])
  await page.screenshot({ path: 'e2e/artifacts/maintenance-edge-grok.png', fullPage: true })
})

test('Codex reset patches account state without duplicate reset or usage requests', async ({ page }) => {
  await page.setViewportSize({ width: 1360, height: 900 })
  const state = await prepare(page)
  let resets = 0
  const singleUsage: string[] = []
  let account = {
    id: 101, name: 'Codex fixture', platform: 'openai', type: 'oauth', status: 'active',
    concurrency: 1, priority: 1, rate_multiplier: 1, schedulable: true, groups: [], credentials: {},
    rate_limited_at: '2026-09-10T00:00:00Z', rate_limit_reset_at: '2099-09-10T00:00:00Z' as string | null,
    created_at: '2026-09-10T00:00:00Z', updated_at: '2026-09-10T00:00:00Z',
    extra: { codex_reset_credit_snapshot: { available_count: 1, credits: [{ expires_at: '2099-09-10T00:00:00Z' }] } },
  }
  page.on('request', request => {
    if (/\/admin\/accounts\/101\/usage/.test(request.url())) singleUsage.push(request.url())
  })
  await page.route('**/api/v1/admin/accounts**', async route => {
    const path = new URL(route.request().url()).pathname
    let data: unknown = {}
    if (path.endsWith('/accounts')) {
      data = { items: [account], total: 1, page: 1, page_size: 20, pages: 1 }
    } else if (path.endsWith('/usage/batch')) {
      data = { usage: { '101': { source: 'passive', five_hour: { utilization: 100 }, seven_day: { utilization: 100 } } } }
    } else if (path.endsWith('/stats')) {
      data = { stats: {} }
    }
    await route.fulfill({ json: { code: 0, data } })
  })
  await page.route('**/api/v1/admin/openai/accounts/101/reset-quota', async route => {
    resets++
    account = { ...account, rate_limit_reset_at: null, updated_at: '2026-09-10T00:01:00Z', extra: { codex_reset_credit_snapshot: { available_count: 0, credits: [] } } }
    await route.fulfill({ json: { code: 0, data: {
      account, windows_reset: 1, cache_refreshed: true,
      quota: { rate_limit_reset_credits: { available_count: 0, credits: [] } },
    } } })
  })
  await page.goto('/admin/accounts')
  const row = page.getByRole('row').filter({ hasText: 'Codex fixture' })
  await expect(row).toContainText('Rate Limited')
  await row.getByRole('button', { name: 'Reset', exact: true }).click()
  await page.getByRole('dialog').getByRole('button', { name: 'Reset', exact: true }).click()
  await expect(page.getByRole('dialog')).toBeHidden()
  await expect(row).toContainText('Reset 1 window(s); credits and account state updated')
  await expect(row).not.toContainText('Rate Limited')
  await expect(row.getByRole('button', { name: 'Reset', exact: true })).toBeDisabled()
  expect(resets).toBe(1)
  expect(singleUsage).toEqual([])
  expect(state.errors).toEqual([])
  await page.screenshot({ path: 'e2e/artifacts/maintenance-codex-reset.png' })
})
