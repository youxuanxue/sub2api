import { defineConfig } from '@playwright/test'
import base from './playwright.config'

export default defineConfig({
  ...base,
  testMatch: '**/us050-candidate-backend.e2e.ts',
  use: { ...base.use, baseURL: 'http://127.0.0.1:4195' },
  webServer: {
    command: 'pnpm exec vite --host 127.0.0.1 --port 4195 --strictPort',
    url: 'http://127.0.0.1:4195',
    reuseExistingServer: false,
    env: { VITE_DEV_PROXY_TARGET: process.env.TK_CANDIDATE_BACKEND_URL! },
  },
})
