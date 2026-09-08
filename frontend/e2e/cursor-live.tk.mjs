import { chromium } from '@playwright/test';
import { readFileSync, writeFileSync, mkdirSync } from 'node:fs';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { resolve } from 'node:path';
import { randomUUID } from 'node:crypto';

const root = fileURLToPath(new URL('../../', import.meta.url));
const state = resolve(root, '.cache/cursor-dev');
const env = JSON.parse(readFileSync(resolve(state, 'env.json'), 'utf8'));
const base = 'http://127.0.0.1:15179';
const out = resolve(state, 'evidence');
mkdirSync(out, { recursive: true, mode: 0o700 });
const browser = await chromium.launch({ headless: true });
const mobile = process.env.CURSOR_E2E_MOBILE === '1';
const context = await browser.newContext({ viewport: mobile ? { width: 390, height: 844 } : { width: 1440, height: 1000 }, locale: 'zh-CN' });
const page = await context.newPage();
page.setDefaultTimeout(20_000);
let auth;
let authorizationID;
async function api(path, method = 'GET', data) {
  const response = await context.request.fetch(`${base}/api/v1${path}`, {
    method, data, headers: { Authorization: `Bearer ${auth}`, 'Idempotency-Key': randomUUID() }
  });
  const body = await response.json();
  if (!response.ok() || body.code !== 0) throw new Error(`${method} ${path}: HTTP ${response.status()} ${body.message || ''}`);
  return body.data;
}
try {
  await page.goto(`${base}/login`);
  await page.getByLabel('邮箱', { exact: true }).fill(env.ADMIN_EMAIL);
  await page.getByLabel('密码', { exact: true }).fill(env.ADMIN_PASSWORD);
  await page.getByRole('button', { name: '登录', exact: true }).click();
  await page.waitForURL(url => url.pathname !== '/login');
  auth = await page.evaluate(() => localStorage.getItem('auth_token'));
  await api('/user/onboarding-tour-completed', 'POST');
  await page.reload();
  if (await page.locator('.driver-popover-close-btn').isVisible()) await page.locator('.driver-popover-close-btn').click();
  const groups = await api('/admin/groups/all');
  let group = groups.find(group => group.name === 'Cursor');
  if (!group) group = await api('/admin/groups', 'POST', { name: 'Cursor', platform: 'newapi', rate_multiplier: 1, subscription_type: 'standard', is_exclusive: false, allow_messages_dispatch: true });
  if (!group.allow_messages_dispatch) await api(`/admin/groups/${group.id}`, 'PUT', { allow_messages_dispatch: true });
  const action = process.argv[2] || 'authorize';
  if (action === 'setup') {
    console.log('Cursor local service group configured');
  } else if (action === 'authorize') {
    await page.goto(`${base}/admin/accounts`);
    const accounts = await api('/admin/accounts?page=1&page_size=100');
    const existing = accounts.items?.find(account => account.name === 'Cursor SDK local verification' && account.extra?.upstream_provider === 'cursor');
    if (existing) await page.getByRole('button', { name: '重新授权 Cursor', exact: true }).first().click();
    else await page.getByTestId('cursor-connect').click();
    await page.locator('#cursor-account-name').fill('Cursor SDK local verification');
    await page.locator('#cursor-service-group').selectOption(String(group.id));
    const started = page.waitForResponse(response => response.url().endsWith('/cursor/authorizations') && response.request().method() === 'POST');
    await page.getByRole('button', { name: '授权 Cursor', exact: true }).click();
    authorizationID = (await (await started).json()).data?.id;
    const link = page.getByRole('link', { name: '打开 Cursor', exact: true });
    await link.waitFor();
    const url = await link.getAttribute('href');
    if (new URL(url).hostname !== 'cursor.com') throw new Error('Unexpected authorization origin');
    spawnSync('open', ['-a', 'Google Chrome', url]);
    console.log('TokenKey UI started official Cursor authorization in the existing Chrome session');
    for (let i = 0; i < 60; i++) {
      if (await page.getByRole('button', { name: '保存', exact: true }).count()) break;
      const script = 'on run argv\n tell application "Google Chrome"\n repeat with w in windows\n repeat with t in tabs of w\n if URL of t is item 1 of argv then\n execute t javascript "Array.from(document.querySelectorAll(\'button\')).find(b => b.textContent.trim() === \'Sign in\')?.click()"\n end if\n end repeat\n end repeat\n end tell\n end run';
      spawnSync('osascript', ['-e', script, url], { encoding: 'utf8', timeout: 5000 });
      await page.waitForTimeout(2000);
    }
    const dialog = page.getByRole('dialog', { name: '连接 Cursor', exact: true });
    const save = dialog.getByRole('button', { name: '保存', exact: true });
    await save.waitFor();
    const status = await dialog.innerText();
    const count = status.match(/(\d+) 个可用模型/)?.[1];
    await page.screenshot({ path: resolve(out, 'authorization-desktop.png') });
    await page.setViewportSize({ width: 390, height: 844 });
    await page.screenshot({ path: resolve(out, 'authorization-mobile.png') });
    await page.setViewportSize({ width: 1440, height: 1000 });
    const [response] = await Promise.all([
      page.waitForResponse(response => response.url().endsWith('/admin/accounts/cursor/import') && response.request().method() === 'POST'),
      save.click()
    ]);
    const result = await response.json();
    if (!response.ok() || result.code !== 0) throw new Error(`Cursor account import failed: HTTP ${response.status()} ${result.message || ''}`);
    await dialog.waitFor({ state: 'hidden' });
    writeFileSync(resolve(out, 'authorization.json'), JSON.stringify({ at: new Date().toISOString(), account: result.data, modelCount: Number(count), groupId: group.id, via: 'TokenKey UI and official Cursor browser authorization' }, null, 2));
    console.log(`Cursor account saved through TokenKey UI: id=${result.data.id}, models=${count}`);
  } else if (action === 'chat' || action === 'chat-all') {
    const user = await page.evaluate(() => JSON.parse(localStorage.getItem('auth_user')));
    const current = await api(`/admin/users/${user.id}`);
    if (current.balance < 10) await api(`/admin/users/${user.id}/balance`, 'POST', { balance: 20, operation: 'add', notes: 'Isolated Cursor development verification' });
    const keys = await api('/keys?page=1&page_size=100');
    let key = keys.items?.find(key => key.name === 'Cursor local verification');
    if (!key) key = await api(`/admin/users/${user.id}/api-keys`, 'POST', { name: 'Cursor local verification', group_id: group.id, expires_in_days: 2, quota: 5 });
    writeFileSync(resolve(state, 'gateway-key.json'), JSON.stringify({ key: key.key, id: key.id, groupId: group.id }), { mode: 0o600 });
    const manifest = JSON.parse(readFileSync(resolve(root, 'backend/internal/service/tk_served_models.json'), 'utf8'));
    const all = Object.entries(manifest.entries).filter(([, entry]) => entry.scopes?.some(scope => scope.channel_type === 14 && scope.base_url === 'http://cursor-bridge:3927')).map(([id]) => id);
    const models = action === 'chat-all' ? all : [process.env.CURSOR_E2E_MODEL || 'composer-2.5'];
    const results = [];
    for (const model of models) {
      await page.goto(`${base}/studio?mode=chat`);
      await page.locator('#studio-key').selectOption(String(key.id));
      await page.locator('#chat-model').waitFor();
      await page.waitForFunction(() => document.querySelector('#chat-model')?.querySelectorAll('option').length > 1);
      const available = await page.locator('#chat-model option').evaluateAll(options => options.map(option => option.value));
      if (!available.includes(model)) {
        results.push({ model, protocol: 'chat_completions', status: 'missing_from_ui' });
        console.log(`${model}: missing from UI (${available.length} options)`);
        continue;
      }
      await page.locator('#chat-model').selectOption(model);
      await page.locator('#chat-max').fill('64');
      await page.locator('textarea').first().fill('Reply with exactly the two ASCII letters OK. Do not translate them into another language. Do not add punctuation.');
      const start = Date.now();
      const [response] = await Promise.all([
        page.waitForResponse(response => new URL(response.url()).pathname.endsWith('/chat/completions'), { timeout: 100_000 }),
        page.getByTestId('studio-chat-send').click()
      ]);
      const body = await response.json();
      await page.getByTestId('studio-chat-send').filter({ hasText: /发送|Send/ }).waitFor();
      const text = body.choices?.[0]?.message?.content || '';
      const result = { model, protocol: 'chat_completions', status: response.ok() && /^OK[.!]?$/i.test(text.trim()) ? 'passed' : 'failed', httpStatus: response.status(), elapsedMs: Date.now() - start, text, usage: body.usage, requestId: response.headers()['x-request-id'], error: body.error?.message };
      results.push(result);
      await page.screenshot({ path: resolve(out, `chat-${model}.png`) });
      if (model === 'composer-2.5' && mobile) {
        await page.screenshot({ path: resolve(out, 'chat-mobile.png'), fullPage: true });
      }
      writeFileSync(resolve(out, `${action}.json`), JSON.stringify({ at: new Date().toISOString(), denominator: models.length, results }, null, 2));
      console.log(`${model}: ${result.status} HTTP ${result.httpStatus} ${result.elapsedMs}ms`);
      if (results.length === 1 && result.status !== 'passed') break;
    }
    if (results.length !== models.length || results.some(result => result.status !== 'passed')) process.exitCode = 1;
  } else {
    throw new Error(`Unknown action: ${action}`);
  }
} catch (error) {
  if (authorizationID && auth) await api(`/admin/accounts/cursor/authorizations/${authorizationID}`, 'DELETE').catch(() => {});
  await page.screenshot({ path: resolve(out, 'failure.png') }).catch(() => {});
  console.error(error.message);
  process.exitCode = 1;
} finally { await context.close(); await browser.close(); }
