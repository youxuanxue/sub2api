import { expect, test, type Page } from '@playwright/test'

// Real account editor and file input; only the backend is a deterministic fixture.
// Persistence/CAS is covered separately with real PostgreSQL integration tests.
const bundle = { format: 'tokenkey-gemini-web-session-v1', user_agent: 'fixture-browser',
  cookies: [{ name: 'SID', value: 'SYNTHETIC_COOKIE', domain: '.google.com', expires: -1 }] }
const upload = (body = JSON.stringify(bundle)) => ({ name: 'session.json', mimeType: 'application/json', buffer: Buffer.from(body) })

async function setup(page: Page) {
  const user = { id: 1, role: 'admin', email: 'operator@example.test', balance: 0, status: 'active' }
  const worker = { id: 28, name: 'Worker fixture A', platform: 'gemini', type: 'apikey',
    concurrency: 1, priority: 1, status: 'active', schedulable: false,
    credentials: {}, credentials_status: { has_api_key: true, has_gemini_web: true, has_gemini_web_runtime: true },
    group_ids: [], supported_protocols: [], extra: {},
    created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z' }
  const accounts = [worker, { ...worker, id: 31, name: 'Worker fixture B' },
    { ...worker, id: 200, name: 'Prod relay fixture', credentials: { gemini_web_relay: true } },
    { ...worker, id: 201, name: 'Plain API fixture', credentials_status: { has_api_key: true, has_gemini_web: false } }]
  const imports: { id: string; body: unknown }[] = []
  let conflict = false
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
      const copy = { ...worker, id: 36, name: 'Copied Worker fixture', schedulable: false,
        credentials_status: { has_api_key: true, has_gemini_web: true, has_gemini_web_runtime: false } }
      accounts.push(copy)
      data = [copy]
    }
    else if (path.endsWith('/gemini-web-session')) {
      const id = path.split('/').at(-2)!
      imports.push({ id, body: route.request().postDataJSON() })
      if (conflict) return route.fulfill({ status: 409, json: { code: 409, message: 'SYNTHETIC_SERVER_SECRET' } })
      const account = accounts.find(a => String(a.id) === id)!
      const initialized = !account.credentials_status.has_gemini_web_runtime
      account.credentials_status.has_gemini_web_runtime = true
      data = { account, session: { mode: initialized ? 'initialized' : 'replaced',
        cookie_count: 1, cookie_domains: ['.google.com'], runtime_version: initialized ? 1 : 8 } }
    } else if (/\/admin\/accounts\/\d+$/.test(path)) data = accounts.find(a => String(a.id) === path.split('/').at(-1))
    else if (path.endsWith('/all')) data = []
    else if (path.includes('/announcements')) data = { items: [], total: 0 }
    await route.fulfill({ json: { code: 0, data } })
  })
  await page.goto('/admin/accounts')
  const open = async (name: string) => {
    await page.locator('tr').filter({ hasText: name }).getByTestId('account-edit-btn').click()
    await expect(page.getByRole('dialog')).toBeVisible()
  }
  return { accounts, imports, open, setConflict: () => { conflict = true } }
}

test('Worker import success, redacted errors, size limit and relay boundary', async ({ page }) => {
  const fixture = await setup(page)
  await fixture.open('Worker fixture A')
  const file = page.getByTestId('gemini-web-session-file')
  await file.setInputFiles(upload('{"value":SYNTHETIC_COOKIE}'))
  await expect(page.getByText('Invalid Gemini Web session JSON', { exact: true })).toBeVisible()
  expect(fixture.imports).toEqual([])
  await file.setInputFiles(upload(' '.repeat(2 * 1024 * 1024 + 1)))
  await expect(page.getByText('Session file exceeds 2 MiB. Export it again.', { exact: true })).toBeVisible()
  expect(fixture.imports).toEqual([])
  await file.setInputFiles(upload())
  await expect(page.getByText('Replaced session with 1 cookies (runtime version 8); scheduling unchanged', { exact: true })).toBeVisible()
  expect(fixture.imports).toEqual([{ id: '28', body: bundle }])
  await expect(page.locator('body')).not.toContainText('SYNTHETIC_COOKIE')
  await page.getByTestId('gemini-web-session-import').scrollIntoViewIfNeeded()
  await page.screenshot({ path: 'e2e/artifacts/gemini-import-success.png' })
  fixture.setConflict()
  await file.setInputFiles(upload())
  await expect(page.getByText('The session changed or the Worker is using it. Retry the import later.', { exact: true })).toBeVisible()
  await expect(page.locator('body')).not.toContainText('SYNTHETIC_SERVER_SECRET')
  await expect(page.getByText('Replaced session with 1 cookies (runtime version 8); scheduling unchanged', { exact: true })).toBeHidden()
  await page.getByRole('button', { name: 'Close modal', exact: true }).click()
  for (const name of ['Prod relay fixture', 'Plain API fixture']) {
    await fixture.open(name)
    await expect(page.getByTestId('gemini-web-session-file')).toHaveCount(0)
    await page.getByRole('button', { name: 'Close modal', exact: true }).click()
  }
})

test('switching accounts during file read cannot submit credentials to either account', async ({ page }) => {
  const fixture = await setup(page)
  await fixture.open('Worker fixture A')
  await page.evaluate(() => {
    const original = File.prototype.text
    File.prototype.text = function () {
      const readFile = original.bind(this)
      return new Promise<string>(resolve => {
        Object.assign(window, { releaseGeminiFile: async () => resolve(await readFile()) })
      })
    }
  })
  await page.getByTestId('gemini-web-session-file').setInputFiles(upload())
  await expect(page.getByTestId('gemini-web-session-file')).toBeDisabled()
  await page.getByRole('button', { name: 'Close modal', exact: true }).click()
  await fixture.open('Worker fixture B')
  await page.evaluate(async () => {
    await (window as unknown as { releaseGeminiFile: () => Promise<void> }).releaseGeminiFile()
  })
  await expect(page.getByTestId('gemini-web-session-file')).toBeEnabled()
  await expect(page.getByText('Replaced session with 1 cookies (runtime version 8); scheduling unchanged', { exact: true })).toBeHidden()
  expect(fixture.imports).toEqual([])
  await page.getByTestId('gemini-web-session-import').scrollIntoViewIfNeeded()
  await page.screenshot({ path: 'e2e/artifacts/gemini-import-account-switch.png' })
})

test('copy Worker and initialize its own session while scheduling stays disabled', async ({ page }) => {
  const fixture = await setup(page)
  await page.locator('tr').filter({ hasText: 'Worker fixture A' }).getByTestId('account-more-btn').click()
  await page.getByRole('button', { name: 'Duplicate Account', exact: true }).click()
  await page.getByRole('dialog').getByRole('button', { name: 'Duplicate Account', exact: true }).click()
  await expect(page.getByRole('dialog')).toBeHidden()
  await fixture.open('Copied Worker fixture')
  await expect(page.getByText('Initialize Gemini Web Worker session', { exact: true })).toBeVisible()
  const name = page.locator('[data-tour="edit-account-form-name"]')
  await name.fill('Renamed copy')
  await page.getByTestId('gemini-web-session-file').setInputFiles(upload())
  await expect(page.getByText('Initialized session with 1 cookies (runtime version 1); scheduling remains disabled. Enable it manually after review.', { exact: true })).toBeVisible()
  expect(fixture.imports).toEqual([{ id: '36', body: bundle }])
  expect(fixture.accounts.find(a => a.id === 36)?.schedulable).toBe(false)
  await expect(page.getByText('Replace Gemini Web browser session', { exact: true })).toBeVisible()
  await expect(name).toHaveValue('Renamed copy')
  await expect(page.locator('body')).not.toContainText('SYNTHETIC_COOKIE')
  await page.getByTestId('gemini-web-session-import').scrollIntoViewIfNeeded()
  await page.screenshot({ path: 'e2e/artifacts/gemini-import-initialize.png' })
})
