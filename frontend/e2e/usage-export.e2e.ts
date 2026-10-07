import { expect, test } from '@playwright/test'
import ExcelJS from 'exceljs'

test('admin exports paginated usage to a readable workbook', async ({ page }, testInfo) => {
  const user = { id: 1449, email: 'admin@example.test', role: 'admin', status: 'active', balance: 100, concurrency: 5, onboarding_tour_seen_at: '2026-09-30T00:00:00Z' }
  await page.addInitScript(user => {
    localStorage.setItem('auth_token', 'fixture-session-only')
    localStorage.setItem('auth_user', JSON.stringify(user))
    localStorage.setItem('tokenkey_locale', 'en')
    localStorage.setItem(`admin_guide_${user.id}_admin_v4_interactive`, 'true')
  }, user)
  const row = { id: 1, created_at: '2026-10-07T00:00:00Z', user: { email: '用户@example.test' }, api_key: { name: '=SUM(1,2)' }, model: 'fixture-model', upstream_model: 'sent-model', upstream_response_model: 'response-model', upstream_model_mismatch: true, input_tokens: 123, output_tokens: 45, rate_multiplier: 1, total_cost: 0.001, actual_cost: 0.001, duration_ms: 120, request_id: 'request-1' }
  const exportPages: number[] = []
  await page.route('**/setup/status', route => route.fulfill({ json: { code: 0, data: { needs_setup: false } } }))
  await page.route('**/api/v1/**', route => {
    const url = new URL(route.request().url())
    const ok = (data: unknown) => route.fulfill({ json: { code: 0, message: 'ok', data } })
    if (url.pathname.endsWith('/auth/me')) return ok(user)
    if (url.pathname.endsWith('/settings/public')) return ok({ site_name: 'TokenKey', custom_menu_items: [], custom_endpoints: [] })
    if (url.pathname === '/api/v1/admin/usage') {
      const p = Number(url.searchParams.get('page') || 1)
      const exportRequest = url.searchParams.get('page_size') === '100'
      if (exportRequest) exportPages.push(p)
      const count = exportRequest && p === 1 ? 100 : 1
      return ok({ items: Array.from({ length: count }, (_, i) => ({ ...row, id: (p - 1) * 100 + i + 1 })), total: 101, page: p, page_size: exportRequest ? 100 : 20, pages: 2 })
    }
    if (url.pathname.endsWith('/stats')) return ok({ total_requests: 101, total_input_tokens: 123, total_output_tokens: 45, total_cost: 0.001, total_actual_cost: 0.001 })
    if (url.pathname.includes('/dashboard/')) return ok({ trend: [], models: [], groups: [] })
    return ok([])
  })
  const workbookRequests: string[] = []
  page.on('request', request => {
    if (request.url().includes('vendor-exceljs')) workbookRequests.push(request.url())
  })
  await page.goto('/admin/usage')
  await expect(page.getByRole('button', { name: /Export.*Excel/i })).toBeVisible()
  expect(workbookRequests).toEqual([])
  const downloadPromise = page.waitForEvent('download')
  await page.getByRole('button', { name: /Export.*Excel/i }).click()
  const download = await downloadPromise
  expect(workbookRequests).toHaveLength(1)
  expect(download.suggestedFilename()).toMatch(/^usage_.*\.xlsx$/)
  const path = await download.path()
  const workbook = new ExcelJS.Workbook()
  await workbook.xlsx.readFile(path!)
  const sheet = workbook.getWorksheet('Usage')!
  expect(sheet.rowCount).toBe(102)
  expect(sheet.getCell('B2').value).toBe('用户@example.test')
  expect(sheet.getCell('C2').value).toBe('=SUM(1,2)')
  expect(sheet.getCell('C2').type).toBe(ExcelJS.ValueType.String)
  expect(sheet.getCell('E2').value).toBe('fixture-model')
  expect(sheet.getCell('F2').value).toBe('sent-model')
  expect(sheet.getCell('G2').value).toBe('response-model')
  expect(sheet.getCell('O2').value).toBe(123)
  expect(exportPages).toEqual([1, 2])
  await expect(page.getByRole('button', { name: 'Cancel Export', exact: true })).toBeHidden()
  await page.screenshot({ path: testInfo.outputPath('usage-export.png'), fullPage: true })
})
