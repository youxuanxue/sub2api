/**
 * Performance audit: navigate every page, collect timing + network data.
 *
 * Usage:
 *   # Local stack (default base URL http://localhost:8080):
 *   E2E_PERF_AUDIT=1 pnpm exec playwright test e2e/perf-audit.e2e.ts
 *
 *   # Prod/staging snapshot (explicit opt-in):
 *   E2E_PERF_AUDIT=1 E2E_BASE_URL=https://api.tokenkey.dev pnpm exec playwright test e2e/perf-audit.e2e.ts
 *
 * Output: e2e/artifacts/perf-report.json
 */
import { test, expect, type Page, type BrowserContext } from '@playwright/test'
import * as fs from 'fs'
import * as path from 'path'
import { fileURLToPath } from 'url'

const __filename = fileURLToPath(import.meta.url)
const __dirname = path.dirname(__filename)

const BASE = process.env.E2E_BASE_URL || 'http://localhost:8080'
const SESSION_TOKEN = process.env.E2E_SESSION_TOKEN || ''
const REFRESH_TOKEN = process.env.E2E_REFRESH_TOKEN || ''

/** Auth pages must stay fast after Stripe/Turnstile lazy-load fixes. */
const AUTH_PAGE_NAV_MS_BUDGET = 10_000

const RUN_PERF_AUDIT = process.env.E2E_PERF_AUDIT === '1'

interface RequestEntry {
  url: string
  method: string
  status: number
  duration: number
  size: number
  isAPI: boolean
}

interface PageMetrics {
  path: string
  role: 'public' | 'user' | 'admin'
  navigationMs: number
  ttfb: number
  domContentLoaded: number
  loadEvent: number
  fcp: number | null
  lcp: number | null
  requestCount: number
  apiRequestCount: number
  totalTransferKB: number
  slowestRequest: { url: string; duration: number } | null
  requests: RequestEntry[]
  error: string | null
}

const PUBLIC_PAGES = [
  '/home',
  '/pricing',
  '/login',
  '/register',
  '/forgot-password',
  '/key-usage',
]

const USER_PAGES = [
  '/dashboard',
  '/keys',
  '/usage',
  '/studio',
  '/available-channels',
  '/profile',
  '/subscriptions',
  '/redeem',
  '/affiliate',
  '/monitor',
]

const ADMIN_PAGES = [
  '/admin/dashboard',
  '/admin/users',
  '/admin/accounts',
  '/admin/channels/pricing',
  '/admin/groups',
  '/admin/subscriptions',
  '/admin/usage',
  '/admin/announcements',
  '/admin/proxies',
  '/admin/redeem',
  '/admin/promo-codes',
  '/admin/settings',
  '/admin/ops',
  '/admin/channels/monitor',
  '/admin/edge-accounts',
  '/admin/orders',
  '/admin/orders/dashboard',
  '/admin/orders/plans',
  '/admin/affiliates/invites',
  '/admin/affiliates/rebates',
  '/admin/affiliates/transfers',
]

async function collectPageMetrics(
  page: Page,
  pagePath: string,
  role: 'public' | 'user' | 'admin',
): Promise<PageMetrics> {
  const requests: RequestEntry[] = []
  const requestTimings = new Map<string, number>()

  page.on('request', (req) => {
    requestTimings.set(req.url(), Date.now())
  })

  page.on('response', async (res) => {
    const url = res.url()
    const startTime = requestTimings.get(url) || Date.now()
    const duration = Date.now() - startTime
    let size = 0
    try {
      const body = await res.body()
      size = body.length
    } catch {
      // streaming or aborted
    }
    const parsedUrl = new URL(url)
    const isAPI =
      parsedUrl.pathname.startsWith('/api/') ||
      parsedUrl.pathname.startsWith('/v1/')
    requests.push({
      url: parsedUrl.pathname + parsedUrl.search,
      method: res.request().method(),
      status: res.status(),
      duration,
      size,
      isAPI,
    })
  })

  const metrics: PageMetrics = {
    path: pagePath,
    role,
    navigationMs: 0,
    ttfb: 0,
    domContentLoaded: 0,
    loadEvent: 0,
    fcp: null,
    lcp: null,
    requestCount: 0,
    apiRequestCount: 0,
    totalTransferKB: 0,
    slowestRequest: null,
    requests: [],
    error: null,
  }

  const turnstilePages = ['/login', '/register', '/forgot-password']
  const waitStrategy = turnstilePages.includes(pagePath)
    ? ('domcontentloaded' as const)
    : ('networkidle' as const)

  try {
    const navStart = Date.now()
    const response = await page.goto(pagePath, {
      waitUntil: waitStrategy,
      timeout: 30_000,
    })
    metrics.navigationMs = Date.now() - navStart

    if (response) {
      metrics.ttfb = (await response.headerValue('x-response-time'))
        ? parseFloat((await response.headerValue('x-response-time'))!)
        : 0
    }

    // Wait a bit for late-firing observers
    await page.waitForTimeout(1500)

    const timing = await page.evaluate(() => {
      const nav = performance.getEntriesByType(
        'navigation',
      )[0] as PerformanceNavigationTiming
      const paintEntries = performance.getEntriesByType('paint')
      const fcpEntry = paintEntries.find(
        (e) => e.name === 'first-contentful-paint',
      )

      let lcp: number | null = null
      try {
        const lcpEntries = performance.getEntriesByType(
          'largest-contentful-paint',
        )
        if (lcpEntries.length > 0) {
          lcp = lcpEntries[lcpEntries.length - 1].startTime
        }
      } catch {
        // LCP not available
      }

      return {
        ttfb: nav ? nav.responseStart - nav.requestStart : 0,
        domContentLoaded: nav
          ? nav.domContentLoadedEventEnd - nav.fetchStart
          : 0,
        loadEvent: nav ? nav.loadEventEnd - nav.fetchStart : 0,
        fcp: fcpEntry ? fcpEntry.startTime : null,
        lcp,
      }
    })

    metrics.ttfb = timing.ttfb || metrics.ttfb
    metrics.domContentLoaded = timing.domContentLoaded
    metrics.loadEvent = timing.loadEvent
    metrics.fcp = timing.fcp
    metrics.lcp = timing.lcp
  } catch (err: any) {
    metrics.error = err.message?.slice(0, 200) || 'unknown error'
  }

  metrics.requests = requests
  metrics.requestCount = requests.length
  metrics.apiRequestCount = requests.filter((r) => r.isAPI).length
  metrics.totalTransferKB = Math.round(
    requests.reduce((s, r) => s + r.size, 0) / 1024,
  )

  const slowest = requests.reduce(
    (max, r) => (r.duration > (max?.duration || 0) ? r : max),
    null as RequestEntry | null,
  )
  if (slowest) {
    metrics.slowestRequest = { url: slowest.url, duration: slowest.duration }
  }

  // Cleanup listeners
  page.removeAllListeners('request')
  page.removeAllListeners('response')

  return metrics
}

test.describe('Performance Audit', () => {
  test.skip(!RUN_PERF_AUDIT, 'set E2E_PERF_AUDIT=1 to run perf audit against E2E_BASE_URL')

  let context: BrowserContext
  let page: Page
  const allMetrics: PageMetrics[] = []

  test.beforeAll(async ({ browser }) => {
    const contextOptions: any = {
      baseURL: BASE,
      ignoreHTTPSErrors: true,
    }

    if (SESSION_TOKEN) {
      contextOptions.storageState = {
        cookies: [],
        origins: [
          {
            origin: BASE,
            localStorage: [
              { name: 'auth_token', value: SESSION_TOKEN },
              ...(REFRESH_TOKEN
                ? [
                    { name: 'refresh_token', value: REFRESH_TOKEN },
                    {
                      name: 'token_expires_at',
                      value: String(Date.now() + 3600_000),
                    },
                  ]
                : []),
            ],
          },
        ],
      }
    }

    context = await browser.newContext(contextOptions)
    page = await context.newPage()
  })

  test.afterAll(async () => {
    const outDir = path.join(__dirname, 'artifacts')
    if (!fs.existsSync(outDir)) fs.mkdirSync(outDir, { recursive: true })

    // Write full JSON
    const reportPath = path.join(outDir, 'perf-report.json')
    fs.writeFileSync(reportPath, JSON.stringify(allMetrics, null, 2))

    // Write summary table to stdout
    console.log('\n' + '='.repeat(100))
    console.log('PERFORMANCE AUDIT SUMMARY')
    console.log('='.repeat(100))
    console.log(
      [
        'Path'.padEnd(35),
        'Role'.padEnd(8),
        'Nav ms'.padStart(8),
        'TTFB'.padStart(8),
        'DCL'.padStart(8),
        'FCP'.padStart(8),
        'LCP'.padStart(8),
        'Reqs'.padStart(6),
        'APIs'.padStart(6),
        'KB'.padStart(8),
        'Slowest'.padStart(8),
      ].join(' '),
    )
    console.log('-'.repeat(100))

    for (const m of allMetrics) {
      if (m.error) {
        console.log(`${m.path.padEnd(35)} ${m.role.padEnd(8)} ERROR: ${m.error}`)
        continue
      }
      console.log(
        [
          m.path.padEnd(35),
          m.role.padEnd(8),
          String(Math.round(m.navigationMs)).padStart(8),
          String(Math.round(m.ttfb)).padStart(8),
          String(Math.round(m.domContentLoaded)).padStart(8),
          (m.fcp !== null ? String(Math.round(m.fcp)) : '-').padStart(8),
          (m.lcp !== null ? String(Math.round(m.lcp)) : '-').padStart(8),
          String(m.requestCount).padStart(6),
          String(m.apiRequestCount).padStart(6),
          String(m.totalTransferKB).padStart(8),
          (m.slowestRequest
            ? String(Math.round(m.slowestRequest.duration))
            : '-'
          ).padStart(8),
        ].join(' '),
      )
    }

    // Flag slow pages
    console.log('\n' + '='.repeat(100))
    console.log('SLOW PAGES (navigation > 3000ms or LCP > 2500ms)')
    console.log('='.repeat(100))
    const slow = allMetrics.filter(
      (m) =>
        !m.error &&
        (m.navigationMs > 3000 || (m.lcp !== null && m.lcp > 2500)),
    )
    if (slow.length === 0) {
      console.log('None detected.')
    } else {
      for (const m of slow) {
        console.log(
          `  ${m.path} — nav: ${Math.round(m.navigationMs)}ms, LCP: ${m.lcp !== null ? Math.round(m.lcp) + 'ms' : 'N/A'}`,
        )
      }
    }

    // Flag heavy API pages
    console.log('\n' + '='.repeat(100))
    console.log('HEAVY API PAGES (> 5 API calls on load)')
    console.log('='.repeat(100))
    const heavy = allMetrics.filter(
      (m) => !m.error && m.apiRequestCount > 5,
    )
    if (heavy.length === 0) {
      console.log('None detected.')
    } else {
      for (const m of heavy) {
        console.log(`  ${m.path} — ${m.apiRequestCount} API calls:`)
        const apiReqs = m.requests.filter((r) => r.isAPI)
        for (const r of apiReqs) {
          console.log(
            `    ${r.method} ${r.url} — ${r.duration}ms (${Math.round(r.size / 1024)}KB)`,
          )
        }
      }
    }

    // Flag slow individual requests
    console.log('\n' + '='.repeat(100))
    console.log('SLOW API REQUESTS (> 1000ms)')
    console.log('='.repeat(100))
    const slowReqs: { page: string; req: RequestEntry }[] = []
    for (const m of allMetrics) {
      for (const r of m.requests) {
        if (r.isAPI && r.duration > 1000) {
          slowReqs.push({ page: m.path, req: r })
        }
      }
    }
    if (slowReqs.length === 0) {
      console.log('None detected.')
    } else {
      for (const { page: pg, req } of slowReqs.sort(
        (a, b) => b.req.duration - a.req.duration,
      )) {
        console.log(
          `  [${pg}] ${req.method} ${req.url} — ${req.duration}ms`,
        )
      }
    }

    console.log('\n' + `Full report: ${reportPath}`)
    await context.close()
  })

  test('audit public pages', async () => {
    test.setTimeout(120_000)
    for (const p of PUBLIC_PAGES) {
      console.log(`  → ${p}`)
      const m = await collectPageMetrics(page, p, 'public')
      allMetrics.push(m)
      expect(m.error, `${p} navigation failed: ${m.error}`).toBeNull()
      if (['/login', '/register', '/forgot-password'].includes(p)) {
        expect(
          m.navigationMs,
          `${p} navigation exceeded ${AUTH_PAGE_NAV_MS_BUDGET}ms budget`,
        ).toBeLessThan(AUTH_PAGE_NAV_MS_BUDGET)
      }
    }
  })

  test('audit user pages', async () => {
    if (!SESSION_TOKEN) {
      test.skip()
      return
    }
    test.setTimeout(180_000)
    for (const p of USER_PAGES) {
      console.log(`  → ${p}`)
      const m = await collectPageMetrics(page, p, 'user')
      allMetrics.push(m)
    }
  })

  test('audit admin pages', async () => {
    if (!SESSION_TOKEN) {
      test.skip()
      return
    }
    test.setTimeout(300_000)
    for (const p of ADMIN_PAGES) {
      console.log(`  → ${p}`)
      const m = await collectPageMetrics(page, p, 'admin')
      allMetrics.push(m)
    }
  })
})
