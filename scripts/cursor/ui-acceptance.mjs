import { readFileSync, mkdirSync, writeFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('../../', import.meta.url));
const require = createRequire(resolve(root, 'frontend/package.json'));
const { chromium, expect } = require('@playwright/test');
const state = resolve(root, '.cache/cursor-dev');
const config = JSON.parse(readFileSync(resolve(state, 'env.json'), 'utf8'));
const evidence = resolve(state, 'native-ui');
mkdirSync(evidence, { recursive: true, mode: 0o700 });
const browser = await chromium.launch({ channel: 'chrome', headless: true });
try {
  const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
  await page.goto('http://127.0.0.1:15179/admin/accounts');
  await page.getByRole('textbox', { name: '邮箱', exact: true }).fill(config.ADMIN_EMAIL);
  await page.getByRole('textbox', { name: '密码', exact: true }).fill(config.ADMIN_PASSWORD);
  await page.getByRole('button', { name: '登录', exact: true }).click();
  await expect(page.getByTestId('cursor-connect')).toBeVisible({ timeout: 30_000 });
  await expect(page.getByRole('button', { name: '登录', exact: true })).toHaveCount(0);
  await page.screenshot({ path: resolve(evidence, 'accounts.png'), fullPage: true });
  writeFileSync(resolve(evidence, 'accounts.txt'), await page.locator('body').innerText());
  console.log('Local admin UI login passed');
} finally {
  await browser.close();
}
