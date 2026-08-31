import { expect, test, type Page, type Route } from '@playwright/test'

const ADMIN = {
  id: 1,
  username: 'supplier-source-admin',
  email: 'supplier-source-admin@tokenkey.test',
  role: 'admin',
  balance: 100,
  concurrency: 10,
  status: 'active',
  allowed_groups: null,
  balance_notify_enabled: false,
  balance_notify_threshold: null,
  balance_notify_extra_emails: [],
  onboarding_tour_seen_at: '2026-08-28T00:00:00Z',
  created_at: '2026-08-28T00:00:00Z',
  updated_at: '2026-08-28T00:00:00Z',
}

interface SupplierModel {
  client_model_id: string
  upstream_model_id: string
  purchase_ratio: number | null
}

interface SupplierSource {
  id: number
  supplier_name: string
  channel_name: string
  endpoint: string
  base_priority: number
  models: SupplierModel[]
  notes: string
  created_at: string
  updated_at: string
}

type ApiRouteHandler = (route: Route, path: string) => Promise<boolean>

function success(data: unknown): string {
  return JSON.stringify({ code: 0, message: 'success', data })
}

async function fulfillSuccess(route: Route, data: unknown, status = 200): Promise<void> {
  await route.fulfill({ status, contentType: 'application/json', body: success(data) })
}

async function fulfillError(route: Route, status: number, message: string, data: unknown): Promise<void> {
  await route.fulfill({
    status,
    contentType: 'application/json',
    body: JSON.stringify({ code: status, message, data }),
  })
}

async function installBase(page: Page, handleApi: ApiRouteHandler): Promise<void> {
  await page.addInitScript((admin) => {
    localStorage.setItem('auth_token', 'us048-e2e-token')
    localStorage.setItem('auth_user', JSON.stringify(admin))
    localStorage.setItem('tokenkey_locale', 'zh')
    localStorage.setItem('theme', 'light')
  }, ADMIN)

  await page.route('**/setup/status', route => fulfillSuccess(route, { needs_setup: false, step: '' }))
  await page.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname
    if (path === '/api/v1/auth/me') {
      await fulfillSuccess(route, ADMIN)
      return
    }
    if (path === '/api/v1/admin/compliance') {
      await fulfillSuccess(route, { required: false, version: 'e2e' })
      return
    }
    if (path === '/api/v1/settings/public') {
      await fulfillSuccess(route, {
        site_name: 'TokenKey',
        backend_mode_enabled: false,
        custom_menu_items: [],
      })
      return
    }
    if (path === '/api/v1/subscriptions/active') {
      await fulfillSuccess(route, [])
      return
    }
    if (path === '/api/v1/announcements') {
      await fulfillSuccess(route, [])
      return
    }
    if (path === '/api/v1/admin/settings') {
      await fulfillSuccess(route, {
        ops_monitoring_enabled: true,
        ops_realtime_monitoring_enabled: true,
        ops_query_mode_default: 'auto',
        custom_menu_items: [],
      })
      return
    }
    if (path === '/api/v1/admin/payment/config') {
      await fulfillSuccess(route, { enabled: false })
      return
    }
    if (path === '/api/v1/keys') {
      await fulfillSuccess(route, { items: [], total: 0, page: 1, page_size: 100, pages: 0 })
      return
    }
    if (path === '/api/v1/admin/system/check-updates') {
      await fulfillSuccess(route, {
        current_version: 'e2e',
        latest_version: 'e2e',
        has_update: false,
        cached: true,
        build_type: 'source',
      })
      return
    }
    if (await handleApi(route, path)) return
    throw new Error(`unexpected API request: ${request.method()} ${path}`)
  })
}

function source(overrides: Partial<SupplierSource> = {}): SupplierSource {
  return {
    id: 7,
    supplier_name: '佳杰',
    channel_name: 'VSTECS',
    endpoint: 'https://token.vstecscloud.com/v1',
    base_priority: 100,
    models: [],
    notes: '',
    created_at: '2026-08-28T00:00:00Z',
    updated_at: '2026-08-28T00:00:00Z',
    ...overrides,
  }
}

test('US048 Jiajie saves facts previews priority and syncs one band account', async ({ page }) => {
  let sources: SupplierSource[] = []
  let submitted: Record<string, unknown> | null = null
  let syncRequests = 0

  await installBase(page, async (route, path) => {
    const request = route.request()
    if (path === '/api/v1/admin/supplier-sources' && request.method() === 'GET') {
      await fulfillSuccess(route, sources)
      return true
    }
    if (path === '/api/v1/admin/supplier-sources' && request.method() === 'POST') {
      submitted = request.postDataJSON() as Record<string, unknown>
      sources = [source({
        channel_name: 'first-batch-lowest-ratio',
        notes: '首批最低合法比例',
        models: [
          {
            client_model_id: 'deepseek-v4-pro',
            upstream_model_id: 'deepseek-v4-pro',
            purchase_ratio: 0.5,
          },
          {
            client_model_id: 'qwen-3.7-max',
            upstream_model_id: 'qwen-3.7-max',
            purchase_ratio: 0.5,
          },
        ],
      })]
      await fulfillSuccess(route, sources[0])
      return true
    }
    if (path === '/api/v1/admin/supplier-sources/priority-preview' && request.method() === 'GET') {
      await fulfillSuccess(route, {
        entries: [{
          source_id: 7,
          supplier_name: '佳杰',
          channel_name: 'first-batch-lowest-ratio',
          discount_band: 3,
          discount_priority: 3,
          priority: 103,
          client_model_ids: ['deepseek-v4-pro', 'qwen-3.7-max'],
        }],
        warnings: [],
      })
      return true
    }
    if (path === '/api/v1/admin/supplier-sources/7/sync' && request.method() === 'POST') {
      syncRequests += 1
      await fulfillSuccess(route, {
        source_id: 7,
        probe_results: [
          {
            client_model_id: 'deepseek-v4-pro',
            upstream_model_id: 'deepseek-v4-pro',
            status: 'passed',
            protocol: 'openai_chat_completions',
          },
          {
            client_model_id: 'qwen-3.7-max',
            upstream_model_id: 'qwen-3.7-max',
            status: 'passed',
            protocol: 'openai_chat_completions',
          },
        ],
        changes: [{
          account_id: 101,
          discount_band: 3,
          action: 'created',
          added_models: ['deepseek-v4-pro', 'qwen-3.7-max'],
          removed_models: [],
          priority_after: 103,
          schedulable_after: true,
        }],
      })
      return true
    }
    return false
  })

  await page.goto('/admin/supplier-sources')
  await page.locator('[data-test="supplier-name"]').fill('佳杰')
  await page.locator('[data-test="channel-name"]').fill('first-batch-lowest-ratio')
  await page.locator('[data-test="endpoint"]').fill('https://token.vstecscloud.com/v1')
  await page.locator('[data-test="credential"]').fill('jiajie-e2e-secret')
  await page.locator('[data-test="notes"]').fill('首批最低合法比例')

  await page.locator('[data-test="client-model-id"]').first().fill('deepseek-v4-pro')
  await page.locator('[data-test="upstream-model-id"]').first().fill('deepseek-v4-pro')
  await page.locator('[data-test="purchase-ratio"]').first().fill('0.5')
  await page.getByRole('button', { name: '添加模型' }).click()
  await page.locator('[data-test="client-model-id"]').nth(1).fill('qwen-3.7-max')
  await page.locator('[data-test="upstream-model-id"]').nth(1).fill('qwen-3.7-max')
  await page.locator('[data-test="purchase-ratio"]').nth(1).fill('0.5')

  await page.locator('[data-test="save-source"]').click()
  await expect(page.locator('[data-test="source-select-7"]')).toBeVisible()
  expect(syncRequests).toBe(0)
  expect(submitted).toEqual({
    supplier_name: '佳杰',
    channel_name: 'first-batch-lowest-ratio',
    endpoint: 'https://token.vstecscloud.com/v1',
    credential: 'jiajie-e2e-secret',
    base_priority: 100,
    models: [
      {
        client_model_id: 'deepseek-v4-pro',
        upstream_model_id: 'deepseek-v4-pro',
        purchase_ratio: 0.5,
      },
      {
        client_model_id: 'qwen-3.7-max',
        upstream_model_id: 'qwen-3.7-max',
        purchase_ratio: 0.5,
      },
    ],
    notes: '首批最低合法比例',
  })

  await page.locator('[data-test="priority-preview-button"]').click()
  const preview = page.locator('[data-test="priority-preview"]')
  await expect(preview).toContainText('佳杰/first-batch-lowest-ratio')
  await expect(preview).toContainText('103')
  await expect(preview).toContainText('deepseek-v4-pro, qwen-3.7-max')

  await page.locator('[data-test="notes"]').fill('尚未保存的运营修改')
  await expect(page.locator('[data-test="sync-source"]')).toBeDisabled()
  await expect(page.locator('[data-test="sync-save-first"]')).toContainText('请先保存再同步')
  expect(syncRequests).toBe(0)
  await page.locator('[data-test="notes"]').fill('首批最低合法比例')
  await expect(page.locator('[data-test="sync-source"]')).toBeEnabled()

  await page.locator('[data-test="sync-source"]').click()
  const result = page.locator('[data-test="sync-result"]')
  await expect(result).toContainText('同步完成')
  await expect(result).toContainText('deepseek-v4-pro → deepseek-v4-pro')
  await expect(result).toContainText('qwen-3.7-max → qwen-3.7-max')
  await expect(result).toContainText('#101 · created · band 3')
  await expect(result).toContainText('priority — → 103')
  expect(syncRequests).toBe(1)
  await expect(page.getByRole('button', { name: /激活|暂停/ })).toHaveCount(0)
})

test('US048 FMGo shows the fixed protocol boundary without account changes', async ({ page }) => {
  const fmgo = source({
    id: 9,
    supplier_name: 'FMGo',
    channel_name: 'seedance-video',
    endpoint: 'https://fmgo.invalid/v1',
    models: [{
      client_model_id: 'doubao-seedance-2-0-260128',
      upstream_model_id: 'feimiao-seedance-2-0-260128',
      purchase_ratio: 0.5,
    }],
  })

  await installBase(page, async (route, path) => {
    const request = route.request()
    if (path === '/api/v1/admin/supplier-sources' && request.method() === 'GET') {
      await fulfillSuccess(route, [fmgo])
      return true
    }
    if (path === '/api/v1/admin/supplier-sources/9/sync' && request.method() === 'POST') {
      await fulfillError(route, 422, 'supplier source probe failed', {
        source_id: 9,
        probe_results: [{
          client_model_id: 'doubao-seedance-2-0-260128',
          upstream_model_id: 'feimiao-seedance-2-0-260128',
          status: 'protocol_unsupported',
          detail: 'supplier protocol unsupported',
        }],
        changes: [],
        failed_step: 'probe',
      })
      return true
    }
    return false
  })

  await page.goto('/admin/supplier-sources')
  await page.locator('[data-test="source-select-9"]').click()
  await expect(page.locator('[data-test="client-model-id"]')).toHaveValue('doubao-seedance-2-0-260128')
  await expect(page.locator('[data-test="upstream-model-id"]')).toHaveValue('feimiao-seedance-2-0-260128')

  await page.locator('[data-test="sync-source"]').click()
  const result = page.locator('[data-test="sync-result"]')
  await expect(result).toContainText('protocol_unsupported')
  await expect(result).toContainText('supplier protocol unsupported')
  await expect(result).toContainText('失败步骤: probe')
  await expect(result).toContainText('本次没有账号变更。')
  await expect(result).not.toContainText('同步完成')
  await expect(page.getByRole('button', { name: /激活|暂停/ })).toHaveCount(0)
})

test('US048 accounts UI marks supplier-managed accounts and explains read-only ownership', async ({ page }) => {
  await installBase(page, async (route, path) => {
    const request = route.request()
    if (path === '/api/v1/admin/accounts' && request.method() === 'GET') {
      await fulfillSuccess(route, {
        items: [{
          id: 101,
          name: '佳杰/VSTECS · 档位 3',
          platform: 'newapi',
          type: 'apikey',
          channel_type: 1,
          status: 'active',
          schedulable: true,
          priority: 103,
          concurrency: 1,
          group_ids: [],
          supported_protocols: ['chat_completions'],
          extra: {
            supplier_source_id: 7,
            supplier_discount_band: 3,
          },
          created_at: '2026-08-28T00:00:00Z',
          updated_at: '2026-08-28T00:00:00Z',
        }],
        total: 1,
        page: 1,
        page_size: 20,
        pages: 1,
      })
      return true
    }
    if (path === '/api/v1/admin/accounts/today-stats/batch' && request.method() === 'POST') {
      await fulfillSuccess(route, { stats: {} })
      return true
    }
    if (path === '/api/v1/admin/accounts/usage/batch' && request.method() === 'POST') {
      await fulfillSuccess(route, { usage: {} })
      return true
    }
    if (path === '/api/v1/admin/accounts/upstream-billing-probe/settings' && request.method() === 'GET') {
      await fulfillSuccess(route, { enabled: true, interval_minutes: 30 })
      return true
    }
    if (path === '/api/v1/admin/proxies/all' && request.method() === 'GET') {
      await fulfillSuccess(route, [])
      return true
    }
    if (path === '/api/v1/admin/groups/all' && request.method() === 'GET') {
      await fulfillSuccess(route, [])
      return true
    }
    if (path === '/api/v1/admin/edge-accounts' && request.method() === 'GET') {
      await fulfillSuccess(route, { platform: '__by_stub__', edges: [], ts: 1 })
      return true
    }
    if (path === '/api/v1/admin/channel-types' && request.method() === 'GET') {
      await fulfillSuccess(route, [])
      return true
    }
    if (path === '/api/v1/admin/supplier-sources' && request.method() === 'GET') {
      await fulfillSuccess(route, [source()])
      return true
    }
    return false
  })

  await page.goto('/admin/accounts')
  const row = page.locator('tr[data-row-id="101"]')
  const badge = row.locator('[data-testid="supplier-managed-badge"]')
  await expect(badge).toHaveText('供应源托管 · 佳杰/VSTECS')
  await expect(badge).toHaveAttribute('href', '/admin/supplier-sources?source_id=7')

  const editButton = row.locator('[data-testid="account-edit-btn"]')
  await expect(editButton).toBeEnabled()
  await expect(editButton).toHaveAttribute('title', '查看托管账号配置（只读）')
  await expect(editButton).toContainText('查看')

  await editButton.click()
  await expect(page.getByRole('heading', { name: '查看账号' })).toBeVisible()
  await expect(page.getByText('以下为只读快照；修改模型、凭证与 priority 请前往供应源管理。')).toBeVisible()
  await expect(page.getByRole('button', { name: '更新' })).toHaveCount(0)
  await page.getByRole('button', { name: '关闭' }).click()

  await row.getByRole('button', { name: '更多' }).click()
  await expect(page.getByText('该账号由供应源托管，请前往供应源管理修改。')).toBeVisible()
  const menuBadge = page.locator('[data-testid="supplier-managed-badge"]').last()
  await expect(menuBadge).toHaveAttribute(
    'href',
    '/admin/supplier-sources?source_id=7',
  )

  await menuBadge.click()
  await expect(page).toHaveURL(/\/admin\/supplier-sources\?source_id=7$/)
  await expect(page.locator('[data-test="source-select-7"]')).toHaveClass(/border-primary-500/)
  await expect(page.locator('[data-test="supplier-name"]')).toHaveValue('佳杰')
  await expect(page.locator('[data-test="channel-name"]')).toHaveValue('VSTECS')
})

test('US048 operator copies a source into a new editor and filters the list', async ({ page }) => {
  const jiajie = source({
    channel_name: 'VSTECS',
    models: [{
      client_model_id: 'deepseek-v4-pro',
      upstream_model_id: 'deepseek-v4-pro',
      purchase_ratio: 0.5,
    }],
  })
  const fmgo = source({
    id: 9,
    supplier_name: 'FMGo',
    channel_name: 'seedance',
    endpoint: 'https://fmgo.invalid/v1',
    models: [{
      client_model_id: 'doubao-seedance-2-0-260128',
      upstream_model_id: 'feimiao-seedance-2-0-260128',
      purchase_ratio: 0.5,
    }],
  })
  let created: Record<string, unknown> | null = null

  await installBase(page, async (route, path) => {
    const request = route.request()
    if (path === '/api/v1/admin/supplier-sources' && request.method() === 'GET') {
      await fulfillSuccess(route, [jiajie, fmgo])
      return true
    }
    if (path === '/api/v1/admin/supplier-sources' && request.method() === 'POST') {
      created = request.postDataJSON() as Record<string, unknown>
      await fulfillSuccess(route, source({
        id: 11,
        channel_name: String(created.channel_name ?? ''),
        models: jiajie.models,
      }))
      return true
    }
    return false
  })

  await page.goto('/admin/supplier-sources')
  await page.locator('[data-test="source-select-7"]').click()
  await expect(page.locator('[data-test="editor-title"]')).toHaveText('编辑供应源')
  await expect(page.locator('[data-test="copy-source"]')).toBeVisible()

  await page.locator('[data-test="copy-source"]').click()
  await expect(page.locator('[data-test="editor-title"]')).toHaveText('复制供应源')
  await expect(page.locator('[data-test="copy-hint"]')).toBeVisible()
  await expect(page.locator('[data-test="copy-source"]')).toHaveCount(0)
  await expect(page.locator('[data-test="sync-source"]')).toHaveCount(0)
  await expect(page.locator('[data-test="source-select-7"]')).not.toHaveClass(/border-primary-500/)
  await expect(page.locator('[data-test="channel-name"]')).toHaveValue('VSTECS (副本)')
  await expect(page.locator('[data-test="credential"]')).toHaveValue('')
  await expect(page.locator('[data-test="client-model-id"]')).toHaveValue('deepseek-v4-pro')

  await page.locator('[data-test="credential"]').fill('copied-e2e-secret')
  await page.locator('[data-test="save-source"]').click()
  await expect(page.locator('[data-test="source-select-11"]')).toBeVisible()
  expect(created).toMatchObject({
    supplier_name: '佳杰',
    channel_name: 'VSTECS (副本)',
    credential: 'copied-e2e-secret',
  })

  await page.locator('[data-test="source-search"]').fill('seedance')
  await expect(page.locator('[data-test="source-list-count"]')).toHaveText('1/3')
  await expect(page.locator('[data-test="source-select-9"]')).toBeVisible()
  await expect(page.locator('[data-test="source-select-7"]')).toHaveCount(0)

  await page.locator('[data-test="source-search"]').fill('no-such-source')
  await expect(page.locator('[data-test="source-search-empty"]')).toBeVisible()
})
