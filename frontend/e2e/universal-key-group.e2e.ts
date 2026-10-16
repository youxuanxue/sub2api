import { expect, test } from '@playwright/test'

test('admin keys keep Universal automatic and allow Direct group changes', async ({ page }) => {
  const user = { id: 1449, email: 'admin@example.test', username: 'admin', role: 'admin', status: 'active', balance: 100, concurrency: 5, allowed_groups: [], onboarding_tour_seen_at: '2026-09-30T00:00:00Z' }
  const groups = [5, 6].map(id => ({ id, name: `Group ${id}`, platform: 'openai', status: 'active', subscription_type: 'standard', rate_multiplier: 1, is_exclusive: false }))
  const keys = ['universal', 'direct'].map((routing_mode, i) => ({ id: i + 1, user_id: user.id, name: `${routing_mode} fixture`, key: 'sk-fixture-only-not-a-real-key', status: 'active', routing_mode, group_id: 5, group: groups[0], created_at: '2026-10-16T00:00:00Z' }))
  const writes: { id: number; group_id: number }[] = []
  await page.addInitScript(user => {
    localStorage.setItem('auth_token', 'fixture-session-only')
    localStorage.setItem('auth_user', JSON.stringify(user))
    localStorage.setItem('tokenkey_locale', 'en')
    localStorage.setItem(`admin_guide_${user.id}_admin_v4_interactive`, 'true')
  }, user)
  await page.route('**/setup/status', route => route.fulfill({ json: { code: 0, data: { needs_setup: false } } }))
  await page.route('**/api/v1/**', async route => {
    const path = new URL(route.request().url()).pathname
    const ok = (data: unknown) => route.fulfill({ json: { code: 0, message: 'ok', data } })
    if (path.endsWith('/auth/me')) return ok(user)
    if (path.endsWith('/settings/public')) return ok({ site_name: 'TokenKey', custom_menu_items: [], custom_endpoints: [] })
    if (path === '/api/v1/admin/users') return ok({ items: [user], total: 1, page: 1, page_size: 20, pages: 1 })
    if (path.endsWith(`/users/${user.id}/api-keys`)) return ok({ items: keys, total: keys.length })
    if (path.endsWith('/groups/all')) return ok(groups)
    if (path.startsWith('/api/v1/admin/api-keys/') && route.request().method() === 'PUT') {
      const id = Number(path.split('/').pop())
      const { group_id } = route.request().postDataJSON()
      writes.push({ id, group_id })
      const key = keys.find(key => key.id === id)!
      Object.assign(key, { group_id, group: groups.find(group => group.id === group_id) })
      return ok({ api_key: key, auto_granted_group_access: false })
    }
    return ok([])
  })
  await page.goto('/admin/users')
  await page.locator('.action-menu-trigger').first().click()
  await page.getByRole('button', { name: 'API Keys', exact: true }).click()
  const dialog = page.getByRole('dialog')
  const universal = dialog.locator('div.rounded-xl.border').filter({ hasText: 'universal fixture' })
  const direct = dialog.locator('div.rounded-xl.border').filter({ hasText: 'direct fixture' })
  await expect(universal).toContainText('Automatic')
  await expect(universal.getByRole('button')).toHaveCount(0)
  await expect(universal).not.toContainText('Group 5')
  await direct.getByRole('button', { name: /Group 5/ }).click()
  await page.getByRole('button', { name: /Group 6/ }).click()
  await expect(direct).toContainText('Group 6')
  expect(writes).toEqual([{ id: 2, group_id: 6 }])
  await expect(universal).toContainText('Automatic')
  await page.screenshot({ path: 'e2e/artifacts/universal-key-group.png', fullPage: true })
})
