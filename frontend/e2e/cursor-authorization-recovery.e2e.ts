import { expect, test } from '@playwright/test'

// Drive the real accounts UI; deterministic API failures isolate poll recovery.
for (const viewport of [{ width: 1440, height: 1000 }, { width: 390, height: 844 }]) {
  test(`Cursor authorization survives a transient disconnect at ${viewport.width}px`, async ({ page }) => {
    await page.setViewportSize(viewport)
    const user = { id: 1, role: 'admin', email: 'cursor-test@example.test', balance: 0, status: 'active' }
    const session = { id: 'cursor-recovery-test', state: 'pending', authorization_url: 'https://cursor.com/loginDeepControl', expires_at: new Date(Date.now() + 600000).toISOString() }
    let starts = 0
    let polls = 0
    let imported: unknown
    await page.addInitScript(user => {
      localStorage.setItem('auth_token', 'cursor-fixture-token')
      localStorage.setItem('auth_user', JSON.stringify(user))
      localStorage.setItem('locale', 'en')
      localStorage.setItem('admin_guide_1_admin_v4_interactive', 'true')
    }, user)
    await page.route('**/setup/status', route => route.fulfill({ json: { code: 0, data: { needs_setup: false } } }))
    await page.route('**/api/v1/**', async route => {
      const path = new URL(route.request().url()).pathname
      let data: unknown = {}
      if (path === '/api/v1/auth/me') data = user
      else if (path.endsWith('/cursor/capabilities')) data = { enabled: true }
      else if (path.endsWith('/cursor/authorizations')) { starts++; data = session }
      else if (path.endsWith(`/cursor/authorizations/${session.id}`)) {
        polls++
        if (polls === 1) return route.abort('connectionreset')
        data = { ...session, state: 'authorized', email: user.email, models: [{ id: 'composer-2.5' }] }
      } else if (path.endsWith('/cursor/import')) {
        imported = route.request().postDataJSON()
        data = { id: 7, name: 'Cursor' }
      } else if (path === '/api/v1/admin/groups/all') data = [{ id: 2, name: 'Cursor', platform: 'newapi' }]
      else if (path === '/api/v1/admin/accounts') data = { items: [], total: 0, page: 1, page_size: 20, pages: 0 }
      else if (path.endsWith('/all')) data = []
      else if (path.includes('/announcements')) data = { items: [], total: 0 }
      await route.fulfill({ json: { code: 0, data } })
    })
    await page.goto('/admin/accounts')
    await page.getByTestId('cursor-connect').click()
    const dialog = page.getByRole('dialog')
    await dialog.getByRole('button', { name: 'Authorize Cursor', exact: true }).click()
    await expect(dialog.getByRole('link', { name: 'Open Cursor', exact: true })).toBeVisible()
    await expect(dialog.getByText(user.email, { exact: true })).toBeVisible()
    expect(starts).toBe(1)
    expect(polls).toBe(2)
    expect(await dialog.evaluate(element => element.scrollWidth <= element.clientWidth)).toBe(true)
    expect(await dialog.evaluate(element => {
      const rect = element.getBoundingClientRect()
      return rect.left >= 0 && rect.right <= window.innerWidth && rect.top >= 0 && rect.bottom <= window.innerHeight
    })).toBe(true)
    await page.screenshot({ path: `e2e/artifacts/cursor-recovery-${viewport.width}.png` })
    await dialog.getByRole('button', { name: 'Save', exact: true }).click()
    await expect(dialog).toBeHidden()
    expect(imported).toEqual({ session_id: session.id, name: 'Cursor', group_ids: [2] })
  })
}
