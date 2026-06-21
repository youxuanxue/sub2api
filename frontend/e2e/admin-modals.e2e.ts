import { test, expect, type Page } from '@playwright/test'

// Admin first-paint smoke — closes the validation gap for PR #900 (latch-lazy-mount of the
// admin Accounts/Users modals). It asserts, against the REAL admin UI:
//
//   (1) DEFERRAL — on /admin/accounts first paint, the account Edit/Create modals' setup()
//       does NOT run, proven by the absence of their setup request (GET
//       /admin/settings/web-search-emulation, which on this page is fired ONLY by those two
//       modals). This is the headline Phase A win and is selector-free / deterministic.
//   (2) LATCH MOUNTS ON DEMAND — clicking a trigger mounts the modal (role="dialog" appears)
//       and closes cleanly. Edit additionally proves DEFERRAL-NOT-REMOVAL: web-search-emulation
//       fires only AFTER the modal opens.
//   (3) Users modals mount on demand the same way.
//
// Run like studio.e2e.ts: against a locally deployed Stage0 stack (E2E_BASE_URL or
// localhost:8080) with an admin login and ≥1 account/user seeded. Not wired into CI (needs a
// live stack). The 28 modals all share the identical `v-if="lazyMount(key, showX)"` pattern, so
// Edit/Import/Create here exercise the mechanism for all; extend the per-trigger checks by
// giving more triggers a data-testid.

const EMAIL = process.env.E2E_EMAIL || 'admin@tokenkey.local'
const PASS = process.env.E2E_PASSWORD || 'Admin12345!'

const WEB_SEARCH_EMULATION = /\/admin\/settings\/web-search-emulation\b/

async function login(page: Page): Promise<void> {
  await page.goto('/login')
  await page.locator('input[type=email]').first().fill(EMAIL)
  await page.locator('input[type=password]').first().fill(PASS)
  await page.locator('button[type=submit]').first().click()
  await page.waitForURL((u) => !u.pathname.includes('/login'), { timeout: 20_000 })
}

// Collect every request URL from now on; returns a live array + a helper.
function recordRequests(page: Page): { urls: string[]; seen: (re: RegExp) => boolean } {
  const urls: string[] = []
  page.on('request', (r) => urls.push(r.url()))
  return { urls, seen: (re: RegExp) => urls.some((u) => re.test(u)) }
}

const dialog = (page: Page) => page.locator('[role="dialog"]')

async function closeDialog(page: Page): Promise<void> {
  // BaseDialog's header button (aria-label="Close modal") deterministically emits close.
  // Fall back to Escape (closeOnEscape default) for any modal without the X.
  const x = page.locator('[role="dialog"] button[aria-label="Close modal"]').last()
  if (await x.count()) {
    await x.click({ timeout: 5_000 }).catch(() => {})
  }
  if ((await dialog(page).count()) > 0) {
    await page.keyboard.press('Escape')
  }
  await expect(dialog(page)).toHaveCount(0, { timeout: 5_000 })
}

test('accounts first paint defers account-modal setup (no web-search-emulation, no dialog)', async ({ page }) => {
  await login(page)
  const rec = recordRequests(page)
  await page.goto('/admin/accounts')
  await page.waitForLoadState('networkidle')

  // No modal is mounted on first paint…
  await expect(dialog(page)).toHaveCount(0)
  // …and the Edit/Create modals' setup request was NOT fired (their setup() never ran).
  expect(rec.seen(WEB_SEARCH_EMULATION), 'web-search-emulation must NOT fire before any modal opens').toBe(false)
  await page.screenshot({ path: 'e2e/artifacts/admin-accounts-first-paint.png', fullPage: true })
})

test('accounts modals mount on demand via the latch (Import; Edit fires setup only on open)', async ({ page }) => {
  await login(page)
  const rec = recordRequests(page)
  await page.goto('/admin/accounts')
  await page.waitForLoadState('networkidle')

  // Import is a toolbar modal (no row needed): clicking mounts it on demand.
  await page.getByTestId('account-import-btn').click()
  await expect(dialog(page)).toBeVisible({ timeout: 10_000 })
  await closeDialog(page)

  // Edit is the heavy modal whose setup() fetches web-search-emulation. It needs a real row;
  // guard on the edit button itself (an empty-state table can still render a <tr>).
  const editBtn = page.getByTestId('account-edit-btn')
  if ((await editBtn.count()) === 0) {
    console.warn('[admin-modals] no account rows seeded — Edit-open check skipped')
    return
  }
  expect(rec.seen(WEB_SEARCH_EMULATION), 'still deferred before opening Edit').toBe(false)
  await editBtn.first().click()
  await expect(dialog(page)).toBeVisible({ timeout: 10_000 })
  // Deferral-not-removal: the setup request fires now that the modal mounted.
  await expect.poll(() => rec.seen(WEB_SEARCH_EMULATION), { timeout: 10_000 }).toBe(true)
  await page.screenshot({ path: 'e2e/artifacts/admin-accounts-edit-open.png', fullPage: true })
  await closeDialog(page)
})

test('users modals mount on demand via the latch (Create; Edit if rows seeded)', async ({ page }) => {
  await login(page)
  await page.goto('/admin/users')
  await page.waitForLoadState('networkidle')
  await expect(dialog(page)).toHaveCount(0)

  await page.getByTestId('user-create-btn').click()
  await expect(dialog(page)).toBeVisible({ timeout: 10_000 })
  await closeDialog(page)

  const userEditBtn = page.getByTestId('user-edit-btn')
  if ((await userEditBtn.count()) === 0) {
    console.warn('[admin-modals] no user rows seeded — Edit-open check skipped')
    return
  }
  await userEditBtn.first().click()
  await expect(dialog(page)).toBeVisible({ timeout: 10_000 })
  await page.screenshot({ path: 'e2e/artifacts/admin-users-edit-open.png', fullPage: true })
  await closeDialog(page)
})
