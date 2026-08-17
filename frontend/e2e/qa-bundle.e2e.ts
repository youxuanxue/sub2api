import { expect, test, type Page, type Route } from '@playwright/test'

const UI_BASE = process.env.E2E_BASE_URL || 'http://127.0.0.1:4173'

const user = {
  id: 7,
  username: 'qa-user',
  email: 'qa@example.test',
  role: 'user',
  balance: 100,
  concurrency: 5,
  status: 'active',
  allowed_groups: null,
  balance_notify_enabled: false,
  balance_notify_threshold: null,
  balance_notify_extra_emails: [],
  onboarding_tour_seen_at: '2026-08-15T00:00:00Z',
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-08-15T00:00:00Z',
  traj_export_enabled: true,
  traj_export_platforms: ['anthropic'],
}

const group = {
  id: 11,
  name: 'Claude',
  description: null,
  platform: 'anthropic',
  rate_multiplier: 1,
  is_exclusive: false,
  status: 'active',
  subscription_type: 'standard',
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
}

const apiKey = {
  id: 42,
  user_id: user.id,
  key: 'sk-qa-e2e',
  name: 'QA E2E Key',
  group_id: group.id,
  group,
  status: 'active',
  ip_whitelist: [],
  ip_blacklist: [],
  last_used_at: null,
  last_used_ip: null,
  quota: 0,
  quota_used: 0,
  expires_at: null,
  created_at: '2026-08-15T00:00:00Z',
  updated_at: '2026-08-15T00:00:00Z',
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

const readyBundle = {
  job_id: 'bundle-job-1',
  status: 'ready',
  api_key_id: apiKey.id,
  data_from: '2026-08-14T11:00:00Z',
  data_until: '2026-08-15T11:00:00Z',
  archive_watermark: '2026-08-15T11:00:00Z',
  record_count: 2,
  pages: [{
    page: 1,
    record_count: 2,
    sha256: 'a'.repeat(64),
    url: `${UI_BASE}/__qa_bundle/page-1.json`,
  }],
}

const bundlePage = {
  schema_version: 'qa-bundle-v1',
  page: 1,
  records: [
    {
      request_id: 'req-claude-1',
      api_key_id: apiKey.id,
      platform: 'anthropic',
      requested_model: 'claude-sonnet-4-6',
      status_code: 200,
      success: true,
      duration_ms: 812,
      input_tokens: 120,
      output_tokens: 48,
      cached_tokens: 0,
      captured_at: '2026-08-15T10:14:00Z',
      detail: { prompt: 'first QA prompt', response: 'first QA response' },
    },
    {
      request_id: 'req-claude-2',
      api_key_id: apiKey.id,
      platform: 'anthropic',
      requested_model: 'claude-opus-4-1',
      status_code: 429,
      success: false,
      duration_ms: 94,
      input_tokens: 20,
      output_tokens: 0,
      cached_tokens: 0,
      captured_at: '2026-08-15T10:44:00Z',
      detail: { error: 'rate limited' },
    },
  ],
}

interface MockOptions {
  entitled?: boolean
  firstBundleUnavailable?: boolean
}

async function json(route: Route, data: unknown, status = 200): Promise<void> {
  await route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(data) })
}

async function installMocks(page: Page, options: MockOptions = {}) {
  const apiRequests: string[] = []
  let bundleAttempts = 0
  let bundlePageFetches = 0
  const entitled = options.entitled !== false

  await page.addInitScript(({ storedUser }) => {
    localStorage.setItem('auth_token', 'qa-e2e-token')
    localStorage.setItem('auth_user', JSON.stringify(storedUser))
    localStorage.setItem('locale', 'en')
  }, {
    storedUser: {
      ...user,
      traj_export_enabled: entitled,
      traj_export_platforms: entitled ? ['anthropic'] : [],
    },
  })

  await page.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const path = url.pathname
    apiRequests.push(`${request.method()} ${path}`)

    if (path === '/api/v1/auth/me') {
      await json(route, { code: 0, data: {
        ...user,
        traj_export_enabled: entitled,
        traj_export_platforms: entitled ? ['anthropic'] : [],
      } })
      return
    }
    if (path === '/api/v1/keys' && request.method() === 'GET') {
      await json(route, { code: 0, data: { items: [apiKey], total: 1, page: 1, page_size: 20, pages: 1 } })
      return
    }
    if (path === '/api/v1/groups/available') {
      await json(route, { code: 0, data: [group] })
      return
    }
    if (path === '/api/v1/groups/rates') {
      await json(route, { code: 0, data: {} })
      return
    }
    if (path === '/api/v1/settings/public') {
      await json(route, { code: 0, data: {} })
      return
    }
    if (path === '/api/v1/usage/dashboard/api-keys-usage') {
      await json(route, { code: 0, data: { stats: {} } })
      return
    }
    if (path === '/api/v1/users/me/qa/bundles' && request.method() === 'POST') {
      bundleAttempts += 1
      if (options.firstBundleUnavailable && bundleAttempts === 1) {
        await json(route, { code: 'QA_BUNDLE_TEMPORARILY_UNAVAILABLE', message: 'temporarily unavailable' }, 503)
      } else {
        await json(route, { code: 0, data: readyBundle })
      }
      return
    }
    if (path === '/api/v1/users/me/qa/bundles/bundle-job-1/export' && request.method() === 'POST') {
      await json(route, { code: 0, data: {
        job_id: 'export-job-1',
        bundle_job_id: readyBundle.job_id,
        status: 'ready',
        record_count: readyBundle.record_count,
        download_url: `${UI_BASE}/__qa_bundle/qa-e2e.zip`,
        expires_at: '2026-08-16T00:00:00Z',
      } })
      return
    }

    await json(route, { code: 'UNEXPECTED_E2E_REQUEST', message: path }, 500)
  })

  await page.route('**/__qa_bundle/page-1.json', async (route) => {
    bundlePageFetches += 1
    await json(route, bundlePage)
  })
  await page.route('**/__qa_bundle/qa-e2e.zip', route => route.fulfill({
    status: 200,
    contentType: 'application/zip',
    headers: { 'Content-Disposition': 'attachment; filename="qa-e2e.zip"' },
    body: 'PK\u0003\u0004qa-e2e',
  }))

  return {
    apiRequests,
    get bundleAttempts() { return bundleAttempts },
    get bundlePageFetches() { return bundlePageFetches },
  }
}

async function openKeys(page: Page): Promise<void> {
  await page.goto(`${UI_BASE}/keys`)
  await expect(page.getByText(apiKey.name)).toBeVisible()
}

function expectNoProdFallback(requests: string[]): void {
  const forbidden = requests.filter(request =>
    request.includes('/users/me/qa/traj/export') ||
    request.includes('/users/me/qa/traj/export/jobs') ||
    request.includes('/users/me/qa/captures') ||
    request.includes('/users/me/qa/proxy'),
  )
  expect(forbidden, `legacy prod QA fallback requests: ${forbidden.join(', ')}`).toEqual([])
}

test('QA Bundle list, detail, watermark and ZIP export stay on Bundle/S3 paths', async ({ page }) => {
  const mock = await installMocks(page)
  await openKeys(page)

  await page.getByRole('button', { name: 'QA', exact: true }).click()
  await expect(page.getByText('QA History')).toBeVisible()
  await expect(page.getByText(/Archived through/)).toBeVisible()
  await expect(page.getByRole('button', { name: /claude-sonnet-4-6/ })).toBeVisible()
  await expect(page.getByText('first QA prompt')).toBeVisible()

  await page.getByRole('button', { name: /claude-opus-4-1/ }).click()
  await expect(page.getByText('rate limited')).toBeVisible()

  const downloadPromise = page.waitForEvent('download')
  await page.getByRole('button', { name: 'Export ZIP' }).click()
  const download = await downloadPromise
  expect(download.suggestedFilename()).toMatch(/^qa-QA_E2E_Key-2026-08-15\.zip$/)

  expect(mock.bundleAttempts).toBe(1)
  expect(mock.bundlePageFetches).toBe(1)
  expect(mock.apiRequests).toContain('POST /api/v1/users/me/qa/bundles')
  expect(mock.apiRequests).toContain('POST /api/v1/users/me/qa/bundles/bundle-job-1/export')
  expectNoProdFallback(mock.apiRequests)
})

test('QA Bundle entitlement denial removes the entry and never starts a job', async ({ page }) => {
  const mock = await installMocks(page, { entitled: false })
  await openKeys(page)

  await expect(page.getByRole('button', { name: 'QA', exact: true })).toHaveCount(0)
  expect(mock.bundleAttempts).toBe(0)
  expectNoProdFallback(mock.apiRequests)
})

test('temporary unavailability is recoverable from the visible retry action', async ({ page }) => {
  const mock = await installMocks(page, { firstBundleUnavailable: true })
  await openKeys(page)

  await page.getByRole('button', { name: 'QA', exact: true }).click()
  await expect(page.getByText('QA history is temporarily unavailable')).toBeVisible()
  await page.getByRole('button', { name: 'Retry' }).click()
  await expect(page.getByRole('button', { name: /claude-sonnet-4-6/ })).toBeVisible()

  expect(mock.bundleAttempts).toBe(2)
  expectNoProdFallback(mock.apiRequests)
})
