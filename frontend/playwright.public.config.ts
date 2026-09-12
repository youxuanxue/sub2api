import { defineConfig, devices } from '@playwright/test'

// Production chunks matter for this regression: the dev server does not merge
// payment side effects into shared vendor chunks. Fixtures stay entirely local.
const dist = process.env.PUBLIC_AUDIT_DIST || '.cache/public-e2e'
const quotedDist = `'${dist.replaceAll("'", "'\\''")}'`
const prepare = process.env.PUBLIC_AUDIT_DIST ? '' : `pnpm exec vite build --outDir ${quotedDist} && `

export default defineConfig({
  testDir: './e2e',
  testMatch: 'public-pages.e2e.ts',
  workers: 1,
  timeout: 30_000,
  expect: { timeout: 5000 },
  reporter: 'list',
  outputDir: '.cache/public-e2e-results',
  use: {
    ...devices['Desktop Chrome'],
    baseURL: 'http://127.0.0.1:4189',
    locale: 'zh-CN',
    channel: process.env.E2E_USE_SYSTEM_CHROME === '1' ? 'chrome' : undefined,
    screenshot: 'only-on-failure',
    trace: 'retain-on-failure',
  },
  webServer: {
    command: `${prepare}pnpm exec vite preview --host 127.0.0.1 --port 4189 --strictPort --outDir ${quotedDist}`,
    url: 'http://127.0.0.1:4189',
    reuseExistingServer: false,
    timeout: 120_000,
  },
})
