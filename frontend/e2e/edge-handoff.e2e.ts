import { expect, test, type Page } from '@playwright/test'
async function login(page: Page) {
  await page.addInitScript(() => { localStorage.setItem('locale', 'en'); localStorage.setItem('admin_guide_7_admin_v4_interactive', 'true'); localStorage.setItem('admin_guide_9_admin_v4_interactive', 'true') })
  await page.goto('/login')
  await page.locator('input[type=email]').fill('admin@example.test')
  await page.locator('input[type=password]').fill('LocalUITest123!')
  await page.locator('button[type=submit]').click()
  await page.waitForURL(url => !url.pathname.includes('/login'))
}
for (const entry of ['overview', 'inline']) {
  test(`existing ${entry} entry opens Edge with clean URL and renewable session`, async ({ page, context }) => {
    await login(page)
    await page.goto(entry === 'overview' ? '/admin/edge-accounts' : '/admin/accounts')
    if (entry === 'inline') {
      await page.getByRole('button', { name: 'Collapse this stub', exact: true }).click()
      await page.getByRole('button', { name: 'Expand this stub', exact: true }).click()
    }
    const button = page.getByRole('button', { name: entry === 'overview' ? 'Manage accounts' : /Manage all edge accounts/i }).first()
    await expect(button).toBeVisible()
    let parentSawToken = false
    page.on('response', async res => {
      if (new URL(res.url()).pathname.endsWith('/admin-session')) {
        const data = await res.json().catch(() => null)
        parentSawToken ||= Boolean(data?.data?.access_token || data?.data?.refresh_token)
      }
    })
    const popupPromise = page.waitForEvent('popup')
    await button.click()
    const child = await popupPromise
    await child.waitForURL('http://127.0.0.1:4322/admin/accounts')
    expect(await child.evaluate(() => Boolean(localStorage.getItem('refresh_token')))).toBe(true)
    expect(await child.evaluate(() => window.opener === null)).toBe(true)
    expect(parentSawToken).toBe(false)
    expect(new URL(page.url()).pathname).toBe(entry === 'overview' ? '/admin/edge-accounts' : '/admin/accounts')
    expect(new URL(child.url()).hash + new URL(child.url()).search).toBe('')
    // No secret-bearing traces, HARs, screenshots or token values retained.
    await child.reload()
    await expect(child).toHaveURL('http://127.0.0.1:4322/admin/accounts')
    await context.clearCookies()
  })
}
test('blocked popup preserves parent and offers retry or direct login', async ({ page }) => {
  await login(page); await page.goto('/admin/edge-accounts')
  await page.evaluate(() => { window.open = () => null })
  await page.getByRole('button', { name: 'Manage accounts', exact: true }).click()
  await expect(page.getByRole('alert').getByRole('button', { name: 'Try again' })).toBeVisible()
  await expect(page.getByRole('alert').getByRole('link', { name: 'Sign in directly' })).toHaveAttribute('href', 'http://127.0.0.1:4322/login')
  await expect(page).toHaveURL(/\/admin\/edge-accounts$/)
})
test('legacy URL is scrubbed and never creates a session', async ({ page }) => {
  await page.goto('http://127.0.0.1:4322/admin/edge-handoff#tk_session=retired&refresh_token=retired')
  await expect(page.getByRole('alert')).toBeVisible()
  expect(await page.evaluate(() => localStorage.getItem('auth_token'))).toBeNull()
  await expect(page).toHaveURL('http://127.0.0.1:4322/admin/edge-handoff')
})

test('failed Edge mint can be retried from the same existing button', async ({ page }) => {
  await login(page); await page.goto('/admin/edge-accounts')
  await page.route('**/api/v1/admin/edge-accounts/local/admin-session', route => route.fulfill({ status: 502, json: { code: 502, message: 'Unavailable' } }), { times: 1 })
  await page.getByRole('button', { name: 'Manage accounts', exact: true }).click()
  await expect(page.getByRole('alert').getByRole('button', { name: 'Try again' })).toBeVisible()
  const popupPromise = page.waitForEvent('popup')
  await page.getByRole('alert').getByRole('button', { name: 'Try again' }).click()
  const child = await popupPromise
  await child.waitForURL('http://127.0.0.1:4322/admin/accounts')
  await expect(page.getByRole('alert')).toHaveCount(0)
})
