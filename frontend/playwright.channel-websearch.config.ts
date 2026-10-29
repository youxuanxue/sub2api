import { defineConfig } from '@playwright/test'
import preview from './playwright.public.config'

export default defineConfig({
  ...preview,
  testMatch: 'channel-websearch.e2e.ts',
  outputDir: '.cache/channel-websearch-results',
})
