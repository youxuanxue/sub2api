import { expect, test } from '@playwright/test'

const user = {
  id: 7, username: 'retirement-admin', email: 'retirement@example.test', role: 'admin',
  balance: 100, concurrency: 5, status: 'active', allowed_groups: [],
  onboarding_tour_seen_at: '2026-01-01T00:00:00Z',
  created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z',
}

test('retired Cursor entries stay absent and supported clients remain usable', async ({ page }) => {
  const requests: string[] = []
  await page.addInitScript(user => {
    localStorage.setItem('auth_token', 'synthetic-retirement-token')
    localStorage.setItem('auth_user', JSON.stringify(user))
    localStorage.setItem('locale', 'en')
    localStorage.setItem('admin_guide_7_admin_v4_interactive', 'true')
  }, user)
  await page.route('**/setup/status', route => route.fulfill({ json: { code: 0, data: { needs_setup: false } } }))
  await page.route('**/api/v1/**', async route => {
    const path = new URL(route.request().url()).pathname
    requests.push(path)
    let data: unknown = {}
    if (path === '/api/v1/auth/me') data = user
    else if (path === '/api/v1/admin/accounts' || path === '/api/v1/keys') data = { items: [], total: 0, page: 1, page_size: 20, pages: 0 }
    else if (path.endsWith('/all') || path.includes('attributes') || path.endsWith('/available')) data = []
    else if (path.includes('announcements')) data = { items: [], total: 0 }
    else if (path === '/api/v1/public/pricing') data = { object: 'list', data: [] }
    await route.fulfill({ json: { code: 0, data } })
  })
  await page.goto('/admin/accounts')
  await expect(page.getByRole('button', { name: 'Create Account', exact: true })).toBeVisible()
  await expect(page.getByTestId('cursor-connect')).toHaveCount(0)
  await expect(page.getByRole('button', { name: /connect cursor/i })).toHaveCount(0)
  expect(requests.filter(path => path.includes('/accounts/cursor/'))).toEqual([])
  await page.screenshot({ path: 'e2e/artifacts/cursor-retirement-accounts.png' })
  // A bookmarked removed client must fall back to a supported configuration.
  await page.goto('/quickstart?client=cursor')
  await expect(page.locator('[data-tk="quickstart-client-picker"]')).toBeVisible()
  await expect(page.locator('[data-tk="quickstart-client-cursor"]')).toHaveCount(0)
  await page.locator('[data-tk="quickstart-client-codex-cli"]').click()
  await expect(page.locator('[data-tk="quickstart-config-workspace"]')).toContainText('Codex')
  await page.screenshot({ path: 'e2e/artifacts/cursor-retirement-quickstart.png' })
})
