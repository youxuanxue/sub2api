import { test, expect, type Page } from '@playwright/test'

const EMAIL = process.env.E2E_EMAIL || 'admin@sub2api.local'
const PASS = process.env.E2E_PASSWORD || 'Admin12345!'

type PublicModel = {
  model_id: string
  pricing?: { billing_mode?: string }
}

async function login(page: Page): Promise<void> {
  await page.goto('/login')
  await page.locator('input[type=email]').first().fill(EMAIL)
  await page.locator('input[type=password]').first().fill(PASS)
  await page.locator('button[type=submit]').first().click()
  await page.waitForURL((u) => !u.pathname.includes('/login'), { timeout: 30_000 })
}

async function publicCatalogCounts(baseURL: string): Promise<{ all: number; image: number; video: number }> {
  const res = await fetch(`${baseURL}/api/v1/public/pricing`)
  if (!res.ok) return { all: 0, image: 0, video: 0 }
  const body = (await res.json()) as { data?: PublicModel[] }
  const models = body.data ?? []
  return {
    all: models.length,
    image: models.filter((m) => m.pricing?.billing_mode === 'image').length,
    video: models.filter((m) => m.pricing?.billing_mode === 'video').length,
  }
}

test.describe('catalog UX — models / pricing / quickstart', () => {
  test('model marketplace media tabs match public billing_mode inventory', async ({ page, baseURL }) => {
    const counts = await publicCatalogCounts(baseURL!)
    test.skip(counts.all === 0, 'public catalog empty in this environment')

    await page.goto('/models')
    await expect(page.getByRole('heading', { level: 1 })).toBeVisible()

    const grid = page.locator('[data-tk="models-marketplace-grid"]')
    await expect(grid).toBeVisible({ timeout: 20_000 })
    const allCards = grid.locator('[data-tk^="models-marketplace-card-"]')
    await expect(allCards.first()).toBeVisible()

    if (counts.image > 0) {
      await page.locator('[data-tk="models-marketplace-tab-image"]').click()
      await expect(page.locator('[data-tk="models-marketplace-empty"]')).toHaveCount(0)
      await expect(page.locator('[data-tk^="models-marketplace-card-"]').first()).toBeVisible()
    }

    if (counts.video > 0) {
      await page.locator('[data-tk="models-marketplace-tab-video"]').click()
      await expect(page.locator('[data-tk="models-marketplace-empty"]')).toHaveCount(0)
      await expect(page.locator('[data-tk^="models-marketplace-card-"]').first()).toBeVisible()
    }
  })

  test('pricing authorized-groups quick start lands on quickstart with model prefilled', async ({ page, baseURL }) => {
    const counts = await publicCatalogCounts(baseURL!)
    test.skip(counts.all === 0, 'public catalog empty in this environment')

    await login(page)

    const res = await fetch(`${baseURL}/api/v1/public/pricing`, {
      headers: { Authorization: `Bearer ${await page.evaluate(() => localStorage.getItem('auth_token'))}` },
    })
    const body = (await res.json()) as { data?: PublicModel[] }
    const sample = body.data?.find((m) => m.model_id) ?? body.data?.[0]
    test.skip(!sample?.model_id, 'no sample model in catalog')

    const modelId = sample.model_id
    await page.goto(`/pricing?model=${encodeURIComponent(modelId)}`)
    await expect(page.locator('#pricing-model-search')).toHaveValue(modelId, { timeout: 20_000 })

    const quickstart = page.locator('[data-tk="pricing-quickstart-for-model"]').first()
    await expect(quickstart).toBeVisible({ timeout: 20_000 })
    await quickstart.click()

    await page.waitForURL((u) => u.pathname === '/quickstart' && u.searchParams.get('model') === modelId, {
      timeout: 20_000,
    })

    await expect(page.locator('[data-tk="use-key-models-empty"]')).toHaveCount(0)
    await expect(page.locator('[data-tk="use-key-model-select"]')).toHaveValue(modelId)
  })

  test('quickstart universal key does not show empty servable-models warning', async ({ page, baseURL }) => {
    const counts = await publicCatalogCounts(baseURL!)
    test.skip(counts.all === 0, 'public catalog empty in this environment')

    await login(page)
    await page.goto('/quickstart')
    await expect(page.locator('[data-tk="use-key-model-select"]')).toBeVisible({ timeout: 30_000 })
    await expect(page.locator('[data-tk="use-key-models-empty"]')).toHaveCount(0)
  })
})
