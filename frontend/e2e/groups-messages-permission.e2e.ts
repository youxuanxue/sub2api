import { expect, test, type Page } from '@playwright/test'

// Real Groups UI with deterministic API boundaries; service persistence is
// covered by TestAntigravityMessagesPermissionAdminLifecycle.
async function fixture(page: Page) {
  const user = { id: 1449, email: 'admin@example.test', role: 'admin', status: 'active', balance: 100, concurrency: 5, onboarding_tour_seen_at: '2026-09-30T00:00:00Z' }
  let group: Record<string, unknown> = { id: 701, name: 'AG permission fixture', platform: 'antigravity', status: 'active', subscription_type: 'standard', rate_multiplier: 1, is_exclusive: false, allow_image_generation: true, allow_messages_dispatch: false, supported_model_scopes: [], account_count: 0, sort_order: 0 }
  const writes: Record<string, unknown>[] = []
  await page.addInitScript(user => {
    localStorage.setItem('auth_token', 'fixture-session-only')
    localStorage.setItem('auth_user', JSON.stringify(user))
    localStorage.setItem('tokenkey_locale', 'en')
    localStorage.setItem(`admin_guide_${user.id}_admin_v4_interactive`, 'true')
  }, user)
  await page.route('**/setup/status', route => route.fulfill({ json: { code: 0, data: { needs_setup: false } } }))
  await page.route('**/api/v1/**', async route => {
    const path = new URL(route.request().url()).pathname
    const method = route.request().method()
    const ok = (data: unknown) => route.fulfill({ json: { code: 0, message: 'ok', data } })
    if (path.endsWith('/auth/me')) return ok(user)
    if (path.endsWith('/settings/public')) return ok({ site_name: 'TokenKey', custom_menu_items: [], custom_endpoints: [], registration_enabled: true })
    if (path === '/api/v1/admin/groups/701' && method === 'PUT' || path === '/api/v1/admin/groups' && method === 'POST') {
      const data = route.request().postDataJSON()
      writes.push(data)
      group = { ...group, ...data }
      return ok(group)
    }
    if (path === '/api/v1/admin/groups') return ok({ items: [group], total: 1, page: 1, page_size: 20, pages: 1 })
    if (path.endsWith('/groups/all')) return ok([group])
    if (path.endsWith('/usage-summary')) return ok({ retained_days: 90, groups: [] })
    if (path.endsWith('/live-capability')) return ok({ supported: false })
    if (path.endsWith('/accounts')) return ok({ items: [], total: 0, page: 1, page_size: 100 })
    return ok([])
  })
  return writes
}

test('AG permission survives edit, save, reopen and explicit disable', async ({ page }) => {
  const writes = await fixture(page)
  await page.goto('/admin/groups')
  await page.getByRole('button', { name: 'Edit', exact: true }).first().click()
  const permission = page.getByTestId('group-messages-permission').getByRole('switch')
  await expect(permission).toHaveAttribute('aria-checked', 'false')
  await permission.click()
  await page.screenshot({ path: 'e2e/artifacts/ag-messages-enabled.png', fullPage: true })
  await page.locator('[data-tour="group-form-submit"]').click()
  await expect.poll(() => writes.length).toBe(1)
  expect(writes[0].allow_messages_dispatch).toBe(true)
  expect(writes[0].allow_image_generation).toBe(true)
  expect(writes[0].messages_dispatch_model_config).toBeUndefined()
  await page.getByRole('button', { name: 'Edit', exact: true }).first().click()
  await expect(permission).toHaveAttribute('aria-checked', 'true')
  await page.locator('[data-tour="edit-group-form-name"]').fill('AG renamed fixture')
  await page.locator('[data-tour="group-form-submit"]').click()
  await expect.poll(() => writes.length).toBe(2)
  expect(writes[1].allow_messages_dispatch).toBe(true)
  await page.getByRole('button', { name: 'Edit', exact: true }).first().click()
  await permission.click()
  await page.locator('[data-tour="group-form-submit"]').click()
  await expect.poll(() => writes.length).toBe(3)
  expect(writes[2].allow_messages_dispatch).toBe(false)
})

test('AG create exposes permission, defaults off and sends explicit opt-in', async ({ page }) => {
  const writes = await fixture(page)
  await page.goto('/admin/groups')
  await page.locator('[data-tour="groups-create-btn"]').click()
  await page.locator('[data-tour="group-form-name"]').fill('AG new fixture')
  await page.locator('[data-tour="group-form-platform"] button').click()
  await page.getByRole('option', { name: /Antigravity/ }).click()
  const permission = page.getByTestId('group-messages-permission').getByRole('switch')
  await expect(permission).toHaveAttribute('aria-checked', 'false')
  await permission.click()
  await page.locator('[data-tour="group-form-submit"]').click()
  await expect.poll(() => writes.length).toBe(1)
  expect(writes[0].platform).toBe('antigravity')
  expect(writes[0].allow_messages_dispatch).toBe(true)
  expect(writes[0].messages_dispatch_model_config).toBeUndefined()
})
