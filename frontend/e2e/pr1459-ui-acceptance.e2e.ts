import { test, expect, type Page } from '@playwright/test'

const EMAIL = process.env.E2E_EMAIL || 'admin@sub2api.local'
const PASS = process.env.E2E_PASSWORD || 'Admin12345!'

async function login(page: Page): Promise<void> {
  await page.goto('/login')
  await page.locator('input[type=email]').first().fill(EMAIL)
  await page.locator('input[type=password]').first().fill(PASS)
  await page.locator('button[type=submit]').first().click()
  await page.waitForURL((u) => !u.pathname.includes('/login'), { timeout: 30_000 })
}

test.describe('PR #1459 UI acceptance', () => {
  test('quickstart: no decorative top border on key section', async ({ page }) => {
    await login(page)
    await page.goto('/quickstart')
    await expect(page.locator('[data-tk="quickstart-key-select"]')).toBeVisible({ timeout: 30_000 })
    await expect(page.locator('section.border-y')).toHaveCount(0)
  })

  test('models browse: switcher shares toolbar row with search (authed)', async ({ page, baseURL }) => {
    await login(page)
    await page.goto('/models')
    await expect(page.locator('[data-tk="catalog-hub-authed"]')).toBeVisible({ timeout: 20_000 })
    await expect(page.locator('[data-tk="catalog-hub-authed-toolbar"]')).toHaveCount(0)

    const switcher = page.locator('[data-tk="catalog-view-switcher"]')
    const search = page.locator('input[placeholder*="搜索"], input[placeholder*="Search"]').first()
    await expect(switcher).toBeVisible({ timeout: 20_000 })
    await expect(search).toBeVisible()

    const switcherBox = await switcher.boundingBox()
    const searchBox = await search.boundingBox()
    expect(switcherBox).toBeTruthy()
    expect(searchBox).toBeTruthy()
    const rowDelta = Math.abs((switcherBox!.y + switcherBox!.height / 2) - (searchBox!.y + searchBox!.height / 2))
    expect(rowDelta).toBeLessThan(40)
  })

  test('models browse card links to quickstart when authed', async ({ page, baseURL }) => {
    const res = await fetch(`${baseURL}/api/v1/public/pricing`)
    test.skip(!res.ok, 'public catalog unavailable')
    const body = (await res.json()) as { data?: { model_id: string }[] }
    const modelId = body.data?.[0]?.model_id
    test.skip(!modelId, 'empty catalog')

    await login(page)
    await page.goto('/models')
    const card = page.locator(`[data-tk="models-marketplace-card-${modelId}"]`)
    await expect(card).toBeVisible({ timeout: 20_000 })

    const href = await card.getAttribute('href')
    expect(href).toContain('/quickstart')
    expect(href).toContain(`model=${encodeURIComponent(modelId)}`)
    expect(href).not.toContain('view=pricing')

    await card.click()
    await page.waitForURL((u) => u.pathname === '/quickstart', { timeout: 20_000 })
    expect(new URL(page.url()).searchParams.get('model')).toBe(modelId)
  })

  test('keys page: filters and actions share one toolbar row', async ({ page }) => {
    await login(page)
    await page.goto('/keys')
    await page.waitForLoadState('networkidle')

    const search = page.locator('input[placeholder*="搜索"], input[placeholder*="Search"]').first()
    const createBtn = page.locator('[data-tour="keys-create-btn"]')
    await expect(search).toBeVisible({ timeout: 20_000 })
    await expect(createBtn).toBeVisible()

    const searchBox = await search.boundingBox()
    const createBox = await createBtn.boundingBox()
    expect(searchBox).toBeTruthy()
    expect(createBox).toBeTruthy()
    const rowDelta = Math.abs((searchBox!.y + searchBox!.height / 2) - (createBox!.y + createBox!.height / 2))
    expect(rowDelta).toBeLessThan(32)
  })

  test('quickstart key picker hides __tk_probe_* names when present', async ({ page }) => {
    await login(page)
    await page.goto('/quickstart')
    const select = page.locator('[data-tk="quickstart-key-select"]')
    await expect(select).toBeVisible({ timeout: 30_000 })

    const options = await select.locator('option').allTextContents()
    for (const label of options) {
      expect(label).not.toMatch(/__tk_probe_/)
    }
  })
})
