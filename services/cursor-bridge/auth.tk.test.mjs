import test from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { CursorAuthorizations, createInternalGuard, secretMatches } from './auth.tk.mjs';

function fixture(t, options) {
  let finish;
  let loginOptions;
  const cursor = {
    auth: { login: opts => {
      loginOptions = opts;
      opts.onLoginUrl('https://cursor.com/loginDeepControl?challenge=test');
      return new Promise(resolve => { finish = resolve; });
    } },
    models: { list: async () => [{ id: 'composer-2.5', variants: [{ params: [{ id: 'fast', value: 'false' }] }] }] },
  };
  const authorizations = new CursorAuthorizations(cursor, options);
  t.after(() => authorizations.close());
  return {
    authorizations, get options() { return loginOptions; },
    async finish() {
      finish({ apiKey: 'secret-user-key', email: 'test@example.invalid', apiKeyExpiresAtMs: Date.now() + 3_600_000 });
      await new Promise(resolve => setImmediate(resolve));
    },
  };
}

test('browser login disables persistence and public status never exposes credentials', async t => {
  const f = fixture(t);
  const session = await f.authorizations.start('admin:1');
  assert.equal(f.options.store, null);
  assert.equal(f.options.openBrowser, false);
  assert.equal(session.state, 'pending');
  await f.finish();
  const status = f.authorizations.status(session.id, 'admin:1');
  assert.equal(status.state, 'authorized');
  assert.equal(status.models[0].id, 'composer-2.5');
  assert.ok(!JSON.stringify(status).includes('secret-user-key'));
  const claimed = f.authorizations.claim(session.id, 'admin:1');
  assert.equal(claimed.api_key, 'secret-user-key');
  assert.throws(() => f.authorizations.claim(session.id, 'admin:1'), { status: 409 });
  f.authorizations.settle(session.id, 'admin:1', claimed.claim, true);
  assert.throws(() => f.authorizations.status(session.id, 'admin:1'), { status: 404 });
});

test('authorization status, cancel and credential claims are owner scoped', async t => {
  const f = fixture(t);
  const session = await f.authorizations.start('admin:1');
  await f.finish();
  for (const method of ['status', 'claim', 'cancel']) {
    assert.throws(() => f.authorizations[method](session.id, 'admin:2'), { status: 404 });
  }
  assert.equal(f.authorizations.status(session.id, 'admin:1').state, 'authorized');
});

test('failed database writes release only the matching claim for retry', async t => {
  const f = fixture(t);
  const session = await f.authorizations.start('admin:1');
  await f.finish();
  const claim = f.authorizations.claim(session.id, 'admin:1');
  assert.throws(() => f.authorizations.settle(session.id, 'admin:1', 'other', false), { status: 409 });
  f.authorizations.settle(session.id, 'admin:1', claim.claim, false);
  assert.equal(f.authorizations.status(session.id, 'admin:1').state, 'authorized');
});

test('expired and cancelled sessions discard credentials and abort polling', async t => {
  let now = 0;
  const f = fixture(t, { now: () => now, ttlMs: 1000 });
  const session = await f.authorizations.start('admin:1');
  await f.finish();
  now = 1001;
  assert.throws(() => f.authorizations.claim(session.id, 'admin:1'), { status: 404 });
  assert.equal(f.options.signal.aborted, true);
  const next = await f.authorizations.start('admin:1');
  f.authorizations.cancel(next.id, 'admin:1');
  assert.equal(f.options.signal.aborted, true);
});

test('authorization session cap and key TTL validation reject before minting', async t => {
  const f = fixture(t, { maxSessions: 1 });
  await assert.rejects(f.authorizations.start('admin:1', { keyTtlMs: 0 }), { status: 400 });
  await f.authorizations.start('admin:1');
  await assert.rejects(f.authorizations.start('admin:2'), { status: 429 });
});

test('internal HTTP authentication is independent of the Cursor API key', async t => {
  const f = fixture(t);
  const secret = 'a'.repeat(40);
  const guard = createInternalGuard({ secret, authorizations: f.authorizations });
  const server = http.createServer(async (req, res) => {
    if (await guard(req, res)) return;
    res.end('forwarded');
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  t.after(() => new Promise(resolve => server.close(resolve)));
  const base = `http://127.0.0.1:${server.address().port}`;
  assert.equal((await fetch(`${base}/health`)).status, 200);
  assert.equal((await fetch(`${base}/v1/messages`, { method: 'POST', headers: { authorization: 'Bearer user-key' } })).status, 401);
  assert.equal((await fetch(`${base}/v1/messages`, { method: 'POST', headers: { 'x-tokenkey-bridge-secret': secret } })).status, 401);
  const headers = { 'x-tokenkey-bridge-secret': secret, 'x-cursor-agent-tenant': 'user:1:key:2:account:3' };
  assert.equal(await (await fetch(`${base}/v1/messages`, { method: 'POST', headers })).text(), 'forwarded');
  assert.equal((await fetch(`${base}/internal/auth`, { method: 'POST', headers, body: '{' })).status, 400);
  assert.equal((await fetch(`${base}/internal/auth`, { method: 'POST', headers, body: 'x'.repeat(20_000) })).status, 413);
  assert.equal(secretMatches('wrong', secret), false);
});
