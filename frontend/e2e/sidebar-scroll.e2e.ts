import { expect, test, type Page, type Route } from '@playwright/test'

const USER = {
  id: 42,
  username: 'sidebar-e2e',
  email: 'sidebar-e2e@tokenkey.local',
  role: 'user',
  balance: 10,
  concurrency: 5,
  status: 'active',
  allowed_groups: null,
  balance_notify_enabled: false,
  balance_notify_threshold: null,
  balance_notify_extra_emails: [],
  created_at: '2026-07-16T00:00:00Z',
  updated_at: '2026-07-16T00:00:00Z',
  onboarding_tour_seen_at: '2026-07-16T00:00:00Z',
}

function success(data: unknown): string {
  return JSON.stringify({ code: 0, message: 'success', data })
}

async function fulfillJSON(route: Route, data: unknown): Promise<void> {
  await route.fulfill({
    status: 200,
    contentType: 'application/json',
    body: success(data),
  })
}

async function prepareAuthedUser(page: Page): Promise<void> {
  await page.setViewportSize({ width: 1280, height: 520 })
  await page.addInitScript((user) => {
    localStorage.setItem('auth_token', 'e2e-sidebar-token')
    localStorage.setItem('auth_user', JSON.stringify(user))
    localStorage.setItem('user_guide_42_user_v4_interactive', 'true')
  }, USER)

  await page.route('**/setup/status', (route) => fulfillJSON(route, { needs_setup: false, step: '' }))
  await page.route('**/api/v1/auth/me', (route) => fulfillJSON(route, USER))
  await page.route('**/api/v1/settings/public', (route) =>
    fulfillJSON(route, {
      site_name: 'TokenKey',
      backend_mode_enabled: false,
      custom_menu_items: [],
      channel_monitor_enabled: true,
      payment_enabled: true,
      available_channels_enabled: true,
      affiliate_enabled: true,
    }),
  )
  await page.route('**/api/v1/subscriptions/active', (route) => fulfillJSON(route, []))
  await page.route('**/api/v1/announcements*', (route) => fulfillJSON(route, []))
  await page.route('**/api/v1/keys*', (route) =>
    fulfillJSON(route, { items: [], total: 0, page: 1, page_size: 100, pages: 0 }),
  )
  await page.route('**/api/v1/user/group-rates*', (route) => fulfillJSON(route, []))
  await page.route('**/api/v1/user/groups*', (route) => fulfillJSON(route, []))
  await page.route('**/api/v1/usage*', (route) =>
    fulfillJSON(route, { items: [], total: 0, page: 1, page_size: 20, pages: 0 }),
  )
  await page.route('**/api/v1/monitor*', (route) => fulfillJSON(route, { items: [], total: 0 }))
  await page.route('**/api/v1/models*', (route) => fulfillJSON(route, { items: [], total: 0 }))
  await page.route('**/api/v1/pricing*', (route) => fulfillJSON(route, { items: [], total: 0 }))
  await page.route('**/api/v1/studio*', (route) => fulfillJSON(route, { items: [], total: 0 }))
  await page.route('**/api/v1/quickstart*', (route) => fulfillJSON(route, { keys: [], groups: [] }))
}

async function sidebarScrollTop(page: Page): Promise<number> {
  return page.locator('nav.sidebar-nav').evaluate((el) => el.scrollTop)
}

async function scrollSidebarDown(page: Page): Promise<number> {
  const max = await page.locator('nav.sidebar-nav').evaluate((el) => Math.max(0, el.scrollHeight - el.clientHeight))
  expect(max, 'sidebar must be tall enough to scroll in this viewport').toBeGreaterThan(120)
  const delta = Math.min(max, 280)
  await page.locator('nav.sidebar-nav').evaluate((el, d) => {
    el.scrollTop = d
  }, delta)
  await page.waitForTimeout(150)
  return sidebarScrollTop(page)
}

async function clickSidebarLink(page: Page, href: string): Promise<void> {
  const link = page.locator(`nav.sidebar-nav a.sidebar-link[href="${href}"]`).first()
  await expect(link).toBeVisible()
  await link.click()
  await page.waitForURL((u) => u.pathname === href || u.pathname.startsWith(`${href}/`), { timeout: 20_000 })
  await page.waitForTimeout(300)
}

test.describe('sidebar scroll preservation (UserShellView)', () => {
  test.beforeEach(async ({ page }) => {
    await prepareAuthedUser(page)
  })

  test('does not reset sidebar scroll to top across console routes', async ({ page }) => {
    await page.goto('/profile')
    await page.locator('nav.sidebar-nav').waitFor()
    expect(await scrollSidebarDown(page)).toBeGreaterThan(100)

    for (const href of ['/usage', '/monitor', '/models', '/keys', '/studio', '/quickstart']) {
      await clickSidebarLink(page, href)
      expect(await sidebarScrollTop(page), `${href} must not jump back to top`).toBeGreaterThan(50)
    }
  })
})
