import { expect, test, type Page, type Route, type TestInfo } from '@playwright/test'

const USER = {
  id: 7,
  username: 'us046-e2e',
  email: 'us046-e2e@tokenkey.test',
  role: 'user',
  balance: 100,
  concurrency: 5,
  status: 'active',
  allowed_groups: null,
  balance_notify_enabled: false,
  balance_notify_threshold: null,
  balance_notify_extra_emails: [],
  onboarding_tour_seen_at: '2026-08-18T00:00:00Z',
  created_at: '2026-08-18T00:00:00Z',
  updated_at: '2026-08-18T00:00:00Z',
}

const AUTOMATIC_KEY = {
  id: 42,
  user_id: USER.id,
  key: 'sk-tokenkey-us046-e2e',
  name: 'Automatic E2E',
  group_id: null,
  routing_mode: 'universal',
  status: 'active',
  ip_whitelist: [],
  ip_blacklist: [],
  last_used_at: null,
  last_used_ip: null,
  quota: 0,
  quota_used: 0,
  expires_at: null,
  created_at: '2026-08-18T00:00:00Z',
  updated_at: '2026-08-18T00:00:00Z',
  current_concurrency: 0,
  rate_limit_5h: 0,
  rate_limit_1d: 0,
  rate_limit_7d: 0,
  usage_5h: 0,
  usage_1d: 0,
  usage_7d: 0,
  window_5h_start: null,
  window_1d_start: null,
  window_7d_start: null,
  reset_5h_at: null,
  reset_1d_at: null,
  reset_7d_at: null,
}

const CAPABILITIES = {
  api_key_id: AUTOMATIC_KEY.id,
  routing_mode: 'universal',
  models: [
    {
      id: 'claude-opus-4-8',
      protocols: ['anthropic'],
      modalities: ['chat'],
      routes: [],
    },
    {
      id: 'gpt-5.5',
      protocols: ['openai', 'codex'],
      modalities: ['chat'],
      routes: [],
    },
  ],
}

function success(data: unknown): string {
  return JSON.stringify({ code: 0, message: 'success', data })
}

async function fulfillSuccess(route: Route, data: unknown): Promise<void> {
  await route.fulfill({
    status: 200,
    contentType: 'application/json',
    body: success(data),
  })
}

async function installFixture(
  page: Page,
  requests: string[],
  capabilityFailure = false,
): Promise<void> {
  await page.setViewportSize({ width: 1280, height: 820 })
  await page.addInitScript((user) => {
    localStorage.setItem('auth_token', 'us046-e2e-token')
    localStorage.setItem('auth_user', JSON.stringify(user))
    localStorage.setItem('theme', 'light')
  }, USER)

  page.on('request', (request) => requests.push(new URL(request.url()).pathname))

  await page.route('**/setup/status', (route) => fulfillSuccess(route, { needs_setup: false, step: '' }))
  await page.route('**/api/v1/auth/me', (route) => fulfillSuccess(route, USER))
  await page.route('**/api/v1/settings/public', (route) =>
    fulfillSuccess(route, {
      api_base_url: new URL(route.request().url()).origin,
      site_name: 'TokenKey',
      custom_menu_items: [],
      custom_endpoints: [],
    }),
  )
  await page.route('**/api/v1/subscriptions/active', (route) => fulfillSuccess(route, []))
  await page.route('**/api/v1/announcements**', (route) => fulfillSuccess(route, []))
  await page.route('**/api/v1/keys?**', (route) =>
    fulfillSuccess(route, {
      items: [AUTOMATIC_KEY],
      total: 1,
      page: 1,
      page_size: 100,
      pages: 1,
    }),
  )
  await page.route(`**/api/v1/me/api-keys/${AUTOMATIC_KEY.id}/capabilities**`, async (route) => {
    if (capabilityFailure) {
      await route.fulfill({
        status: 500,
        contentType: 'application/json',
        body: JSON.stringify({ code: 50001, message: 'capability unavailable' }),
      })
      return
    }
    await fulfillSuccess(route, CAPABILITIES)
  })
  await page.route('**/api/v1/public/pricing**', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        object: 'list',
        data: [
          {
            model_id: 'claude-opus-4-8',
            pricing: { currency: 'USD', input_per_1k_tokens: 0, output_per_1k_tokens: 0 },
          },
          {
            model_id: 'gpt-5.5',
            pricing: { currency: 'USD', input_per_1k_tokens: 0, output_per_1k_tokens: 0 },
          },
        ],
        updated_at: '2026-08-18T00:00:00Z',
      }),
    }),
  )
}

async function optionValues(page: Page): Promise<string[]> {
  return page.locator('[data-tk="use-key-model-select"] option').evaluateAll((options) =>
    options.map((option) => (option as HTMLOptionElement).value),
  )
}

function expectCapabilityOnly(requests: string[]): void {
  expect(requests.filter((path) => path === `/api/v1/me/api-keys/${AUTOMATIC_KEY.id}/capabilities`).length)
    .toBeGreaterThan(0)
  expect(requests).not.toContain('/api/v1/me/pricing-catalog')
  expect(requests).not.toContain('/v1/models')
}

async function expectNoHorizontalOverflow(page: Page): Promise<void> {
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
}

test.describe('US-046 automatic-routing capability discovery', () => {
  test('US046 automatic routing key uses the capability menu', async ({ page }, testInfo: TestInfo) => {
    const requests: string[] = []
    await installFixture(page, requests)

    await page.goto('/quickstart?client=claude-code')
    const modelSelect = page.locator('[data-tk="use-key-model-select"]')
    await expect(modelSelect).toBeVisible({ timeout: 30_000 })
    await expect.poll(() => optionValues(page)).toEqual(['claude-opus-4-8'])

    await page.locator('[data-tk="quickstart-client-codex-cli"]').click()
    await expect.poll(() => optionValues(page)).toEqual(['gpt-5.5'])
    await expectNoHorizontalOverflow(page)
    await page.screenshot({ path: testInfo.outputPath('quickstart-capability-menu.png'), fullPage: true })

    await page.goto('/studio')
    const studioModel = page.locator('#chat-model')
    await expect(studioModel).toBeVisible({ timeout: 30_000 })
    await expect(studioModel.locator('option')).toHaveCount(2)
    await expect(studioModel.locator('option')).toHaveText(['claude-opus-4-8', 'gpt-5.5'])
    await expectNoHorizontalOverflow(page)
    await page.screenshot({ path: testInfo.outputPath('studio-capability-menu.png'), fullPage: true })

    expectCapabilityOnly(requests)
  })

  test('capability failure is visible instead of becoming an empty menu', async ({ page }) => {
    const requests: string[] = []
    await installFixture(page, requests, true)

    await page.goto('/quickstart?client=claude-code')
    await expect(page.locator('[data-tk="use-key-models-error"]')).toBeVisible({ timeout: 30_000 })
    await expect(page.locator('[data-tk="use-key-models-empty"]')).toHaveCount(0)
    await expect(page.locator('[data-tk="use-key-model-select"] option')).toHaveCount(0)
    await expect(page.locator('[data-tk="quickstart-send-test"]')).toHaveCount(0)
    await expect(page.locator('pre code')).toHaveCount(0)
    await page.locator('[data-tk="use-key-models-retry"]').click()
    await expect.poll(() => requests.filter((url) =>
      url.includes(`/me/api-keys/${AUTOMATIC_KEY.id}/capabilities`)).length).toBe(2)

    await page.goto('/studio')
    await expect(page.locator('[data-testid="studio-load-error"]')).toContainText('capability unavailable', {
      timeout: 30_000,
    })
    await expect(page.locator('#chat-model')).toHaveCount(0)

    expectCapabilityOnly(requests)
  })
})
