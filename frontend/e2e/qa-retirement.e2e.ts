import { expect, test } from '@playwright/test'

const user = {
  id: 7, username: 'raw-only-user', email: 'raw-only@example.test', role: 'admin',
  balance: 100, concurrency: 5, rpm_limit: 0, status: 'active', allowed_groups: [],
  onboarding_tour_seen_at: '2026-01-01T00:00:00Z',
  created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z',
  // An old cached entitlement must never resurrect the retired product entry.
  traj_export_enabled: true, traj_export_platforms: ['anthropic'],
}
const group = { id: 11, name: 'Claude', platform: 'anthropic', status: 'active', rate_multiplier: 1 }
const apiKey = {
  id: 42, user_id: user.id, key: 'sk-synthetic-raw-only', name: 'Raw capture key',
  group_id: group.id, group, status: 'active', quota: 0, quota_used: 0,
  ip_whitelist: [], ip_blacklist: [], current_concurrency: 0,
  created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z',
}

for (const viewport of [{ width: 1440, height: 1000 }, { width: 390, height: 844 }]) {
  test(`QA retirement preserves key and admin edits at ${viewport.width}px`, async ({ page }) => {
    await page.setViewportSize(viewport)
    const requests: string[] = []
    let updatedUser: Record<string, unknown> | undefined
    await page.addInitScript(user => {
      localStorage.setItem('auth_token', 'synthetic-raw-only-token')
      localStorage.setItem('auth_user', JSON.stringify(user))
      localStorage.setItem('locale', 'en')
      localStorage.setItem('admin_guide_7_admin_v4_interactive', 'true')
    }, user)
    await page.route('**/setup/status', route => route.fulfill({ json: { code: 0, data: { needs_setup: false } } }))
    await page.route('**/api/v1/**', async route => {
      const request = route.request()
      const path = new URL(request.url()).pathname
      requests.push(path)
      let data: unknown = {}
      if (path === '/api/v1/auth/me') data = user
      else if (path === '/api/v1/keys') data = { items: [apiKey], total: 1, page: 1, page_size: 20, pages: 1 }
      else if (path === '/api/v1/groups/available' || path === '/api/v1/admin/groups/all') data = [group]
      else if (path === '/api/v1/usage/dashboard/api-keys-usage') data = { stats: {} }
      else if (path === '/api/v1/admin/users') data = { items: [user], total: 1, page: 1, page_size: 20, pages: 1 }
      else if (path === '/api/v1/admin/users/7' && request.method() === 'PUT') {
        updatedUser = request.postDataJSON()
        data = { ...user, ...updatedUser }
      } else if (path.includes('attributes') || path.endsWith('/all')) data = []
      else if (path.includes('/announcements')) data = { items: [], total: 0 }
      await route.fulfill({ json: { code: 0, data } })
    })
    await page.goto('/keys')
    await expect(page.getByText(apiKey.name, { exact: true })).toBeVisible()
    await expect(page.getByRole('button', { name: /QA|export.*history/i })).toHaveCount(0)
    await page.getByRole('button', { name: 'Edit', exact: true }).click()
    await expect(page.getByRole('dialog')).toBeVisible()
    await expect(page.getByRole('dialog').getByRole('textbox').first()).toHaveValue(apiKey.name)
    await page.getByRole('dialog').getByRole('button', { name: 'Cancel', exact: true }).click()
    await expect(page.getByRole('dialog')).toBeHidden()
    await page.screenshot({ path: `e2e/artifacts/raw-only-keys-${viewport.width}.png` })
    await page.goto('/admin/users')
    await page.getByTestId('user-edit-btn').click()
    const dialog = page.getByRole('dialog')
    await expect(dialog).toBeVisible()
    await expect(dialog.getByText(/QA.*export|trajectory.*export/i)).toHaveCount(0)
    await dialog.locator('input[type=email]').fill('updated-raw-only@example.test')
    await dialog.getByRole('button', { name: 'Update', exact: true }).click()
    await expect(dialog).toBeHidden()
    expect(updatedUser?.email).toBe('updated-raw-only@example.test')
    expect(updatedUser).not.toHaveProperty('traj_export_enabled')
    expect(requests.filter(path => path.includes('/users/me/qa/'))).toEqual([])
    await page.screenshot({ path: `e2e/artifacts/raw-only-users-${viewport.width}.png` })
  })
}
