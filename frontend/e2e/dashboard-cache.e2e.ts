import { expect, test, type Page, type Route } from '@playwright/test'

const ADMIN = {
  id: 1,
  username: 'cache-admin',
  email: 'cache-admin@tokenkey.local',
  role: 'admin',
  balance: 100,
  concurrency: 10,
  status: 'active',
  allowed_groups: null,
  balance_notify_enabled: false,
  balance_notify_threshold: null,
  balance_notify_extra_emails: [],
  created_at: '2026-07-16T00:00:00Z',
  updated_at: '2026-07-16T00:00:00Z',
}

const GROUPS = [
  {
    group_id: 102,
    group_name: 'Claude Kiro',
    requests: 120,
    input_tokens: 1_180_000,
    output_tokens: 20_000,
    cache_creation_tokens: 0,
    cache_read_tokens: 0,
    cache_telemetry_unavailable_input_tokens: 1_180_000,
    total_tokens: 1_200_000,
    cost: 0,
    actual_cost: 0,
    account_cost: 0,
  },
  {
    group_id: 101,
    group_name: 'GPT Production',
    requests: 100,
    input_tokens: 150_000,
    output_tokens: 50_000,
    cache_creation_tokens: 50_000,
    cache_read_tokens: 750_000,
    cache_telemetry_unavailable_input_tokens: 0,
    total_tokens: 1_000_000,
    cost: 0,
    actual_cost: 0,
    account_cost: 0,
  },
  {
    group_id: 103,
    group_name: 'DeepSeek',
    requests: 90,
    input_tokens: 480_000,
    output_tokens: 40_000,
    cache_creation_tokens: 80_000,
    cache_read_tokens: 200_000,
    cache_telemetry_unavailable_input_tokens: 0,
    total_tokens: 800_000,
    cost: 0,
    actual_cost: 0,
    account_cost: 0,
  },
  {
    group_id: 104,
    group_name: 'Gemini',
    requests: 80,
    input_tokens: 300_000,
    output_tokens: 50_000,
    cache_creation_tokens: 50_000,
    cache_read_tokens: 200_000,
    cache_telemetry_unavailable_input_tokens: 0,
    total_tokens: 600_000,
    cost: 0,
    actual_cost: 0,
    account_cost: 0,
  },
  {
    group_id: 105,
    group_name: 'Grok',
    requests: 70,
    input_tokens: 260_000,
    output_tokens: 40_000,
    cache_creation_tokens: 50_000,
    cache_read_tokens: 150_000,
    cache_telemetry_unavailable_input_tokens: 0,
    total_tokens: 500_000,
    cost: 0,
    actual_cost: 0,
    account_cost: 0,
  },
  {
    group_id: 106,
    group_name: 'Small OpenAI',
    requests: 20,
    input_tokens: 100_000,
    output_tokens: 20_000,
    cache_creation_tokens: 30_000,
    cache_read_tokens: 50_000,
    cache_telemetry_unavailable_input_tokens: 0,
    total_tokens: 200_000,
    cost: 0,
    actual_cost: 0,
    account_cost: 0,
  },
  {
    group_id: 107,
    group_name: 'Tiny Anthropic',
    requests: 10,
    input_tokens: 70_000,
    output_tokens: 10_000,
    cache_creation_tokens: 10_000,
    cache_read_tokens: 10_000,
    cache_telemetry_unavailable_input_tokens: 0,
    total_tokens: 100_000,
    cost: 0,
    actual_cost: 0,
    account_cost: 0,
  },
]

const DASHBOARD_STATS = {
  total_users: 10,
  today_new_users: 1,
  active_users: 5,
  hourly_active_users: 2,
  stats_updated_at: '2026-07-16T08:00:00Z',
  stats_stale: false,
  total_api_keys: 8,
  active_api_keys: 7,
  total_accounts: 12,
  normal_accounts: 11,
  error_accounts: 1,
  ratelimit_accounts: 0,
  overload_accounts: 0,
  total_requests: 1_000,
  total_input_tokens: 2_540_000,
  total_output_tokens: 230_000,
  total_cache_creation_tokens: 270_000,
  total_cache_read_tokens: 1_360_000,
  total_tokens: 4_400_000,
  total_cost: 10,
  total_actual_cost: 8,
  total_account_cost: 7,
  today_requests: 500,
  today_input_tokens: 2_540_000,
  today_output_tokens: 230_000,
  today_cache_creation_tokens: 270_000,
  today_cache_read_tokens: 1_360_000,
  today_tokens: 4_400_000,
  today_cost: 5,
  today_actual_cost: 4,
  today_account_cost: 3.5,
  average_duration_ms: 250,
  uptime: 3_600,
  rpm: 12,
  tpm: 25_000,
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

async function prepareDashboard(page: Page, viewport: { width: number; height: number }): Promise<URL> {
  let snapshotURL: URL | null = null
  await page.setViewportSize(viewport)
  await page.addInitScript((admin) => {
    localStorage.setItem('auth_token', 'e2e-admin-token')
    localStorage.setItem('auth_user', JSON.stringify(admin))
  }, ADMIN)

  await page.route('**/setup/status', (route) => fulfillJSON(route, { needs_setup: false, step: '' }))
  await page.route('**/api/v1/auth/me', (route) => fulfillJSON(route, ADMIN))
  await page.route('**/api/v1/admin/compliance', (route) =>
    fulfillJSON(route, {
      required: false,
      version: 'e2e',
      document_path_zh: '',
      document_path_en: '',
      document_url_zh: '',
      document_url_en: '',
      ack_phrase_zh: '',
      ack_phrase_en: '',
    })
  )
  await page.route('**/api/v1/settings/public', (route) =>
    fulfillJSON(route, { site_name: 'TokenKey', backend_mode_enabled: false, custom_menu_items: [] })
  )
  await page.route('**/api/v1/subscriptions/active', (route) => fulfillJSON(route, []))
  await page.route('**/api/v1/announcements*', (route) => fulfillJSON(route, []))
  await page.route('**/api/v1/keys*', (route) =>
    fulfillJSON(route, { items: [], total: 0, page: 1, page_size: 100, pages: 0 })
  )
  await page.route('**/api/v1/admin/dashboard/snapshot-v2*', (route) => {
    snapshotURL = new URL(route.request().url())
    return fulfillJSON(route, {
      generated_at: '2026-07-16T08:00:00Z',
      start_date: '2026-07-15',
      end_date: '2026-07-16',
      granularity: 'hour',
      stats: DASHBOARD_STATS,
      trend: [],
      models: [],
      groups: GROUPS,
      users_trend: [],
    })
  })
  await page.route('**/api/v1/admin/dashboard/users-ranking*', (route) =>
    fulfillJSON(route, {
      ranking: [],
      total_actual_cost: 0,
      total_requests: 0,
      total_tokens: 0,
      start_date: '2026-07-15',
      end_date: '2026-07-16',
    })
  )

  await page.goto('/admin/dashboard')
  await expect(page.getByTestId('prompt-cache-card')).toBeVisible({ timeout: 20_000 })
  expect(snapshotURL).not.toBeNull()
  return snapshotURL!
}

async function assertCacheCard(page: Page, screenshotName: string): Promise<void> {
  const card = page.getByTestId('prompt-cache-card')
  const rows = card.getByTestId('prompt-cache-group-row')

  await expect(rows).toHaveCount(5)
  await expect(rows).toHaveText([
    /Claude Kiro/,
    /GPT Production/,
    /DeepSeek/,
    /Gemini/,
    /Grok/,
  ])
  await expect(card.getByText('Small OpenAI')).toHaveCount(0)
  await expect(card.getByText('Tiny Anthropic')).toHaveCount(0)

  const kiroRow = rows.filter({ hasText: 'Claude Kiro' })
  await expect(kiroRow).toContainText(/不可观测|Not observable/i)
  await expect(kiroRow).not.toContainText(/(?:^|\s)0(?:\.0)?%/)

  const overflow = await page.evaluate(() => ({
    document: document.documentElement.scrollWidth - document.documentElement.clientWidth,
    card: (() => {
      const el = document.querySelector<HTMLElement>('[data-testid="prompt-cache-card"]')
      return el ? el.scrollWidth - el.clientWidth : 0
    })(),
  }))
  expect(overflow.document, 'page must not overflow horizontally').toBeLessThanOrEqual(1)
  expect(overflow.card, 'cache card must not overflow horizontally').toBeLessThanOrEqual(1)

  await card.screenshot({ path: `e2e/artifacts/${screenshotName}` })
}

test.describe('admin prompt-cache group observability', () => {
  test('desktop ranks by impact and treats Kiro as unobservable', async ({ page }) => {
    const snapshotURL = await prepareDashboard(page, { width: 1440, height: 900 })
    await assertCacheCard(page, 'dashboard-cache-desktop.png')

    await page.getByTestId('prompt-cache-group-row').filter({ hasText: 'GPT Production' }).click()
    await page.waitForURL(/\/admin\/usage\?/)
    const usageURL = new URL(page.url())
    expect(usageURL.searchParams.get('group_id')).toBe('101')
    expect(usageURL.searchParams.get('start_ts')).toBe(snapshotURL.searchParams.get('start_ts'))
    expect(usageURL.searchParams.get('end_ts')).toBe(snapshotURL.searchParams.get('end_ts'))
  })

  test('390px mobile keeps the ranked cache card within the viewport', async ({ page }) => {
    await prepareDashboard(page, { width: 390, height: 844 })
    await assertCacheCard(page, 'dashboard-cache-mobile-390.png')
  })
})
