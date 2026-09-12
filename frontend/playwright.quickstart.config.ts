import { defineConfig } from '@playwright/test'
import base from './playwright.config'

export default defineConfig({
  ...base,
  testMatch: '**/public-quickstart.e2e.ts',
  timeout: 45_000,
  use: { ...base.use, baseURL: 'http://127.0.0.1:4196', trace: 'off', video: 'off', screenshot: 'only-on-failure' },
  webServer: {
    command: 'pnpm exec vite --host 127.0.0.1 --port 4196 --strictPort',
    url: 'http://127.0.0.1:4196',
    reuseExistingServer: false,
  },
})
