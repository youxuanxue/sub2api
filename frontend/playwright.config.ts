import { defineConfig } from '@playwright/test'

/**
 * Template/source contracts: no browser; fast regression on Vue SFC structure.
 * Optional browser E2E can extend this config later (webServer + storageState).
 */
export default defineConfig({
  testDir: './e2e',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  reporter: [['list']],
  projects: [
    {
      name: 'template-contracts',
      testMatch: '**/template-contracts.spec.ts',
    },
  ],
})
