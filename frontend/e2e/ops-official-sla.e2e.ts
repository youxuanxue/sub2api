import { expect, test } from '@playwright/test'

// Real admin dashboard against a sanitized, deterministic overview response.
for (const [label, scope, display] of [
  ['official', { requests: 1982317, faults: 5014, rate: 1 - 5014 / 1982317 }, '99.747%'],
  ['trial only', { requests: 0, faults: 0, rate: null }, '-'],
  ['ordinary site', undefined, '76.130%'],
] as const) {
  test(`Ops SLA card: ${label}`, async ({ page }) => {
    const admin = { id: 1, role: 'admin', email: 'sla@example.test', username: 'sla', status: 'active' }
    await page.addInitScript(user => {
      localStorage.setItem('auth_token', 'sla-test')
      localStorage.setItem('auth_user', JSON.stringify(user))
      localStorage.setItem('tokenkey_locale', 'zh')
    }, admin)
    const overview = { sla: 0.7613, official_sla: scope, request_count_total: 2597302, error_count_sla: 619976, success_count: 1951092, qps: {}, tps: {}, duration: {}, ttft: {} }
    await page.route('**/setup/status', route => route.fulfill({ json: { code: 0, data: { needs_setup: false } } }))
    await page.route('**/api/v1/**', route => {
      const path = new URL(route.request().url()).pathname
      let data: unknown = []
      if (path.endsWith('/auth/me')) data = admin
      if (path.endsWith('/settings/public')) data = { site_name: 'TokenKey', custom_menu_items: [] }
      if (path.endsWith('/admin/settings')) data = { ops_monitoring_enabled: true, ops_realtime_monitoring_enabled: false }
      if (path.endsWith('/dashboard/snapshot-v2')) data = { overview, throughput_trend: { points: [] }, error_trend: { points: [] } }
      if (path.endsWith('/dashboard/overview')) data = overview
      return route.fulfill({ json: { code: 0, data } })
    })
    await page.goto('/admin/ops')
    const card = page.getByTestId('ops-sla-card')
    await expect(card.getByText(display, { exact: true }).first()).toBeVisible()
    if (scope) await expect(card.getByText(/正式运营 SLA|Official SLA/)).toBeVisible()
  })
}
