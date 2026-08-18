import { expect, test, type APIRequestContext, type Page, type Route } from '@playwright/test'

const API_BASE = process.env.E2E_API_BASE_URL || process.env.E2E_BASE_URL || 'http://127.0.0.1:3000'
const LOGIN_EMAIL = process.env.E2E_LOGIN_EMAIL || 'admin@sub2api.local'
const LOGIN_PASSWORD = process.env.E2E_LOGIN_PASSWORD || 'Admin12345!'

const GROUP_ID = 701
const GROUP = {
  id: GROUP_ID,
  name: 'Canonical Usage Group',
  description: null,
  platform: 'anthropic',
  rate_multiplier: 1,
  rpm_limit: 0,
  is_exclusive: false,
  status: 'active',
  subscription_type: 'standard',
  daily_limit_usd: null,
  weekly_limit_usd: null,
  monthly_limit_usd: null,
  allow_image_generation: false,
  image_rate_independent: false,
  image_rate_multiplier: 1,
  image_price_1k: null,
  image_price_2k: null,
  image_price_4k: null,
  claude_code_only: false,
  fallback_group_id: null,
  fallback_group_id_on_invalid_request: null,
  allow_messages_dispatch: false,
  default_mapped_model: '',
  require_oauth_only: false,
  require_privacy_set: false,
  model_routing: null,
  model_routing_enabled: false,
  mcp_xml_inject: true,
  supported_model_scopes: [],
  account_count: 3,
  active_account_count: 2,
  rate_limited_account_count: 0,
  sort_order: 10,
  created_at: '2026-08-01T00:00:00Z',
  updated_at: '2026-08-01T00:00:00Z',
}

const USAGE_SUMMARY = {
  group_id: GROUP_ID,
  today_cost: 1.23,
  yesterday_cost: 45.67,
  total_cost: 890.12,
}

async function loginSession(request: APIRequestContext) {
  const loginResponse = await request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { email: LOGIN_EMAIL, password: LOGIN_PASSWORD },
  })
  expect(loginResponse.ok(), `login failed: ${loginResponse.status()}`).toBeTruthy()
  const loginBody = await loginResponse.json()
  expect(loginBody.code, `login API error: ${loginBody.message}`).toBe(0)

  const token = loginBody.data.access_token as string
  const meResponse = await request.get(`${API_BASE}/api/v1/auth/me`, {
    headers: { Authorization: `Bearer ${token}` },
  })
  expect(meResponse.ok(), `auth/me failed: ${meResponse.status()}`).toBeTruthy()
  const meBody = await meResponse.json()
  expect(meBody.code).toBe(0)
  expect(meBody.data.role).toBe('admin')

  return { token, user: meBody.data as Record<string, unknown> }
}

async function fulfillJSON(route: Route, data: unknown): Promise<void> {
  await route.fulfill({
    status: 200,
    contentType: 'application/json',
    body: JSON.stringify({ code: 0, message: 'success', data }),
  })
}

async function prepareGroupsPage(
  page: Page,
  request: APIRequestContext,
  viewport: { width: number; height: number },
): Promise<() => number> {
  const { token, user } = await loginSession(request)
  let usageSummaryRequests = 0

  await page.setViewportSize(viewport)
  await page.addInitScript(
    ({ accessToken, storedUser }) => {
      localStorage.setItem('auth_token', accessToken)
      localStorage.setItem('auth_user', JSON.stringify(storedUser))
      localStorage.setItem('locale', 'en')
      localStorage.setItem('group-hidden-columns', JSON.stringify([]))
      localStorage.setItem('group-column-settings-version', '2')
      const userID = typeof storedUser.id === 'number' ? storedUser.id : 0
      if (userID > 0) {
        localStorage.setItem(`admin_guide_${userID}_admin_v4_interactive`, 'true')
      }
    },
    {
      accessToken: token,
      storedUser: {
        ...user,
        onboarding_tour_seen_at:
          (user.onboarding_tour_seen_at as string | undefined) || '2026-08-18T00:00:00Z',
      },
    },
  )

  await page.route('**/api/v1/admin/groups**', async (route) => {
    const requestURL = new URL(route.request().url())
    const path = requestURL.pathname

    if (path === '/api/v1/admin/groups/usage-summary') {
      usageSummaryRequests += 1
      await fulfillJSON(route, [USAGE_SUMMARY])
      return
    }
    if (path === '/api/v1/admin/groups/capacity-summary') {
      await fulfillJSON(route, [])
      return
    }
    if (path === '/api/v1/admin/groups/live-capability') {
      await fulfillJSON(route, { supported: false, reason: 'e2e fixture' })
      return
    }
    if (path.endsWith('/models-list-candidates')) {
      await fulfillJSON(route, [])
      return
    }
    if (path === '/api/v1/admin/groups') {
      await fulfillJSON(route, {
        items: [GROUP],
        total: 1,
        page: 1,
        page_size: 20,
        pages: 1,
      })
      return
    }

    await route.continue()
  })

  await page.goto('/admin/groups')
  await expect(page.getByTestId('group-usage-summary')).toBeVisible({ timeout: 30_000 })
  return () => usageSummaryRequests
}

async function assertCanonicalSummary(page: Page): Promise<void> {
  const summary = page.getByTestId('group-usage-summary')
  await expect(summary).toHaveAttribute('data-group-id', String(GROUP_ID))

  const expectedItems = [
    { testID: 'group-usage-today', label: 'Today', value: '$1.23' },
    { testID: 'group-usage-yesterday', label: 'Yesterday', value: '$45.67' },
    { testID: 'group-usage-total', label: 'Total', value: '$890.1' },
  ]
  const items = summary.locator(':scope > [data-testid]')
  await expect(items).toHaveCount(expectedItems.length)
  for (const [index, expected] of expectedItems.entries()) {
    const item = items.nth(index)
    await expect(item).toHaveAttribute('data-testid', expected.testID)
    await expect(item.locator('span').nth(0)).toHaveText(expected.label)
    await expect(item.locator('span').nth(1)).toHaveText(expected.value)
  }
}

for (const scenario of [
  { name: 'desktop table', viewport: { width: 1360, height: 900 } },
  { name: 'mobile card', viewport: { width: 390, height: 844 } },
]) {
  test(`${scenario.name} renders Today, Yesterday, Total from one summary request`, async ({ page, request }) => {
    const usageRequestCount = await prepareGroupsPage(page, request, scenario.viewport)

    await assertCanonicalSummary(page)
    expect(usageRequestCount()).toBe(1)

    for (const testID of ['group-usage-today', 'group-usage-yesterday', 'group-usage-total']) {
      const item = page.getByTestId(testID)
      await item.scrollIntoViewIfNeeded()
      await expect(item).toBeVisible()
    }

    const hasUnexpectedHorizontalOverflow = await page.evaluate(
      () => document.documentElement.scrollWidth > document.documentElement.clientWidth + 1,
    )
    expect(hasUnexpectedHorizontalOverflow).toBe(false)
  })
}
