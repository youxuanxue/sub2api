import { expect, test } from '@playwright/test'
import { readFile } from 'node:fs/promises'
import ExcelJS from 'exceljs'
import type { HistoricalBillingReport, HistoricalBillingRow } from '../src/api/historicalBilling'

for (const role of ['user', 'admin'] as const) {
  test(`${role} queries retained historical bills and downloads matching CSV and Excel`, async ({ page }, testInfo) => {
    const user = { id: role === 'user' ? 16 : 1449, email: `${role}@example.test`, role, status: 'active', balance: 100, concurrency: 5, onboarding_tour_seen_at: '2026-09-30T00:00:00Z' }
    await page.addInitScript(user => {
      localStorage.setItem('auth_token', 'fixture-session-only')
      localStorage.setItem('auth_user', JSON.stringify(user))
      localStorage.setItem('tokenkey_locale', 'en')
      localStorage.setItem(`admin_guide_${user.id}_admin_v4_interactive`, 'true')
    }, user)
    const row: HistoricalBillingRow = { date: '2026-06-04', platform: 'openai', requests: 2, input_tokens: 10, output_tokens: 20, cache_creation_tokens: 30, cache_read_tokens: 40, total_tokens: 100, actual_cost: '0.3000000003', computed_at: '2026-10-29T00:00:00Z', provisional: false }
    const today: HistoricalBillingRow = { ...row, date: '2026-10-29', platform: 'anthropic', actual_cost: '1.25', provisional: true }
    const requests: URL[] = []
    let fail = false
    await page.route('**/setup/status', route => route.fulfill({ json: { code: 0, data: { needs_setup: false } } }))
    await page.route('**/api/v1/**', route => {
      const url = new URL(route.request().url())
      const ok = (data: unknown) => route.fulfill({ json: { code: 0, data } })
      if (url.pathname.endsWith('/auth/me')) return ok(user)
      if (url.pathname.endsWith('/settings/public')) return ok({ site_name: 'TokenKey', custom_menu_items: [], custom_endpoints: [] })
      if (url.pathname.endsWith('/users/16')) return ok({ id: 16, email: 'customer@example.test' })
      if (url.pathname.endsWith('/billing-history')) {
        requests.push(url)
        if (fail) return route.fulfill({ status: 503, json: { message: 'Fixture query failure' } })
        const platform = url.searchParams.get('platform')
        const month = url.searchParams.get('granularity') === 'month'
        const rows = [row, today].filter(item => platform === null || item.platform === platform).map(item => ({ ...item, date: month ? item.date.slice(0, 7) : item.date }))
        const report: HistoricalBillingReport = {
          user_id: 16, registered_at: '2026-06-04T00:00:00Z', start_date: '2026-06-04', end_date: '2026-10-29',
          timezone: 'UTC', currency: 'USD', granularity: month ? 'month' : 'day', platform,
          platforms: ['anthropic', 'openai'], available_start_date: '2026-06-04', available_end_date: '2026-10-29', generated_at: '2026-10-29T01:00:00Z', rows,
          totals: platform === 'openai' ? row : { ...row, requests: 4, input_tokens: 20, output_tokens: 40, cache_creation_tokens: 60, cache_read_tokens: 80, total_tokens: 200, actual_cost: '1.5500000003', provisional: true },
        }
        return ok(report)
      }
      if (url.pathname.endsWith('/usage')) return ok({ items: [], total: 0, page: 1, pages: 0 }) // no retained raw logs
      if (url.pathname.endsWith('/stats')) return ok({ total_requests: 0, total_actual_cost: 0 })
      if (url.pathname.includes('/dashboard/')) return ok({ trend: [], models: [], groups: [] })
      return ok([])
    })
    await page.goto(role === 'admin' ? '/admin/usage?user_id=16&start_ts=1793152260000&end_ts=1793238660000' : '/usage')
    const panel = page.getByTestId('historical-billing')
    expect(requests).toEqual([])
    await panel.getByRole('button', { name: 'View historical bill' }).click()
    await expect(panel.getByRole('cell', { name: '2026-06-04', exact: true })).toBeVisible()
    await expect(panel.getByRole('cell', { name: '1.5500000003', exact: true })).toBeVisible()
    await expect(panel.getByText('Period in progress').first()).toBeVisible()
    expect(requests[0].searchParams.has('start_ts')).toBe(false)
    expect(requests[0].searchParams.get('user_id')).toBe(role === 'admin' ? '16' : null)
    await panel.getByLabel('Group by', { exact: true }).selectOption('month')
    await expect(panel.getByRole('cell', { name: '2026-06', exact: true })).toBeVisible()
    await panel.getByLabel('Platform', { exact: true }).selectOption('openai')
    await expect(panel.getByRole('cell', { name: '0.3000000003', exact: true })).toHaveCount(2)
    const beforeDownloads = requests.length
    const csvDownload = page.waitForEvent('download')
    await panel.getByRole('button', { name: 'Download CSV' }).click()
    const csv = await csvDownload
    expect(csv.suggestedFilename()).toBe('billing_user-16_2026-06-04_2026-10-29_month.csv')
    const content = await readFile((await csv.path())!, 'utf8')
    const headers = ['User', 'Date / month', 'Platform', 'Requests', 'Input tokens', 'Output tokens', 'Cache write tokens', 'Cache read tokens', 'Total tokens', 'Actual charge (USD)', 'Billing timezone']
    expect(content.replace(/^\uFEFF/, '').split('\r\n')).toEqual([
      headers.map(header => `"${header}"`).join(','),
      '"16","2026-06","openai","2","10","20","30","40","100","0.3000000003","UTC"',
      '"16","Total","","2","10","20","30","40","100","0.3000000003","UTC"',
      '',
    ])
    const excelDownload = page.waitForEvent('download')
    await panel.getByRole('button', { name: 'Download Excel' }).click()
    const excel = await excelDownload
    const workbook = new ExcelJS.Workbook()
    await workbook.xlsx.readFile((await excel.path())!)
    const sheet = workbook.getWorksheet('Billing')!
    expect(sheet.getCell('B2').value).toBe('2026-06')
    expect(sheet.getCell('J2').value).toBe('0.3000000003')
    expect(sheet.getCell('J3').value).toBe('0.3000000003')
    expect(sheet.getRow(1).values).toEqual([undefined, ...headers])
    expect(sheet.getRow(2).values).toEqual([undefined, 16, '2026-06', 'openai', 2, 10, 20, 30, 40, 100, '0.3000000003', 'UTC'])
    expect(sheet.getRow(3).values).toEqual([undefined, 16, 'Total', '', 2, 10, 20, 30, 40, 100, '0.3000000003', 'UTC'])
    expect(requests).toHaveLength(beforeDownloads)
    await page.screenshot({ path: testInfo.outputPath(`${role}-historical-bill.png`), fullPage: true })
    await page.setViewportSize({ width: 390, height: 844 })
    await expect(panel.getByRole('button', { name: 'Download CSV' })).toBeVisible()
    expect(await panel.evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true)
    await page.screenshot({ path: testInfo.outputPath(`${role}-historical-bill-mobile.png`), fullPage: true })
    fail = true
    await panel.getByRole('button', { name: 'Refresh bill' }).click()
    await expect(panel.getByRole('alert')).toContainText('Could not load the bill')
    await expect(panel.getByRole('table')).toHaveCount(0)
    await expect(panel.getByRole('button', { name: 'Download CSV' })).toHaveCount(0)
    fail = false
    await panel.getByRole('button', { name: 'Refresh bill' }).click()
    await expect(panel.getByRole('cell', { name: '0.3000000003', exact: true })).toHaveCount(2)
  })
}
