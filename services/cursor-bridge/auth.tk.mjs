import { randomUUID, timingSafeEqual } from 'node:crypto';

export function secretMatches(actual, expected) {
  if (typeof actual !== 'string' || typeof expected !== 'string') return false;
  const left = Buffer.from(actual);
  const right = Buffer.from(expected);
  return right.length >= 32 && left.length === right.length && timingSafeEqual(left, right);
}

function failure(status, message) { return Object.assign(new Error(message), { status }); }

export class CursorAuthorizations {
  constructor(cursor, { ttlMs = 600_000, maxSessions = 16, now = Date.now } = {}) {
    this.cursor = cursor;
    this.ttlMs = ttlMs;
    this.maxSessions = maxSessions;
    this.now = now;
    this.sessions = new Map();
    this.timer = setInterval(() => this.sweep(), Math.min(ttlMs, 30_000));
    this.timer.unref();
  }

  sweep() {
    for (const [id, session] of this.sessions) {
      if (session.expiresAt <= this.now()) this.remove(id);
    }
  }

  remove(id) {
    const session = this.sessions.get(id);
    session?.controller.abort();
    if (session) session.credentials = undefined;
    this.sessions.delete(id);
  }

  get(id, owner) {
    this.sweep();
    const session = this.sessions.get(id);
    if (!session || session.owner !== owner) throw failure(404, 'Authorization session not found');
    return session;
  }

  async start(owner, { name = 'TokenKey Cursor', keyTtlMs = 90 * 86_400_000 } = {}) {
    this.sweep();
    if (!owner) throw failure(401, 'Missing authorization owner');
    if (this.sessions.size >= this.maxSessions) throw failure(429, 'Too many authorization sessions');
    if (!Number.isInteger(keyTtlMs) || keyTtlMs < 3_600_000 || keyTtlMs > 90 * 86_400_000) {
      throw failure(400, 'API key lifetime must be between one hour and ninety days');
    }
    const session = {
      id: randomUUID(), owner, state: 'pending', expiresAt: this.now() + this.ttlMs,
      controller: new AbortController(),
    };
    this.sessions.set(session.id, session);
    let showUrl;
    let rejectUrl;
    const urlReady = new Promise((resolve, reject) => { showUrl = resolve; rejectUrl = reject; });
    session.controller.signal.addEventListener('abort', () => rejectUrl(failure(410, 'Authorization expired')), { once: true });
    Promise.resolve().then(() => this.cursor.auth.login({
      store: null, openBrowser: false,
      apiKeyName: String(name).slice(0, 80), apiKeyTtlMs: keyTtlMs,
      signal: session.controller.signal,
      onLoginUrl(url) {
        const target = new URL(url);
        if (target.protocol !== 'https:' || target.hostname !== 'cursor.com' || target.username || target.password) {
          throw failure(502, 'Unexpected Cursor authorization URL');
        }
        session.url = target.href;
        showUrl();
      },
    })).then(async credentials => {
      if (session.controller.signal.aborted) return;
      session.credentials = credentials;
      const models = await this.cursor.models.list({ apiKey: credentials.apiKey });
      if (session.controller.signal.aborted) return;
      session.models = models;
      session.state = 'authorized';
    }).catch(() => {
      session.credentials = undefined;
      session.state = 'failed';
      session.error = 'Cursor authorization or model discovery failed';
      rejectUrl(failure(502, session.error));
    });
    await urlReady;
    return this.status(session.id, owner);
  }

  status(id, owner) {
    const session = this.get(id, owner);
    return {
      id, state: session.state, authorization_url: session.url,
      expires_at: new Date(session.expiresAt).toISOString(),
      key_expires_at: session.credentials?.apiKeyExpiresAtMs
        ? new Date(session.credentials.apiKeyExpiresAtMs).toISOString() : undefined,
      email: session.credentials?.email, models: session.models, error: session.error,
    };
  }

  claim(id, owner) {
    const session = this.get(id, owner);
    if (session.state !== 'authorized') throw failure(409, 'Authorization is not ready or already claimed');
    session.state = 'claimed';
    session.claim = randomUUID();
    return { ...this.status(id, owner), claim: session.claim, api_key: session.credentials.apiKey };
  }

  settle(id, owner, claim, success) {
    const session = this.get(id, owner);
    if (!claim || session.claim !== claim || session.state !== 'claimed') throw failure(409, 'Invalid credential claim');
    if (success) this.remove(id);
    else { session.state = 'authorized'; session.claim = undefined; }
  }

  cancel(id, owner) { this.get(id, owner); this.remove(id); }
  close() { clearInterval(this.timer); for (const id of this.sessions.keys()) this.remove(id); }
}

export async function readJSON(req, maxBytes = 16_384) {
  let size = 0;
  const chunks = [];
  for await (const chunk of req) {
    size += chunk.length;
    if (size > maxBytes) throw failure(413, 'Request body too large');
    chunks.push(chunk);
  }
  try { return JSON.parse(Buffer.concat(chunks).toString() || '{}'); }
  catch { throw failure(400, 'Invalid JSON'); }
}

export function json(res, status, body) {
  res.writeHead(status, { 'Content-Type': 'application/json', 'Cache-Control': 'no-store' });
  res.end(JSON.stringify(body));
}

export function createInternalGuard({ secret, authorizations }) {
  if (!secret || Buffer.byteLength(secret) < 32) throw new Error('CURSOR_BRIDGE_SECRET must contain at least 32 bytes');
  return async (req, res) => {
    const path = new URL(req.url, 'http://localhost').pathname;
    if (req.method === 'GET' && path === '/health') return false;
    if (!secretMatches(req.headers['x-tokenkey-bridge-secret'], secret)) {
      json(res, 401, { error: { type: 'authentication_error', message: 'Invalid bridge credential' } });
      return true;
    }
    const owner = req.headers['x-cursor-agent-tenant'];
    if (typeof owner !== 'string' || !/^[a-zA-Z0-9:_-]{1,200}$/.test(owner)) {
      json(res, 401, { error: { type: 'authentication_error', message: 'Missing trusted owner' } });
      return true;
    }
    if (!path.startsWith('/internal/auth')) return false;
    try {
      const parts = path.split('/').filter(Boolean);
      if (parts.length === 2 && req.method === 'POST') {
        json(res, 202, await authorizations.start(owner, await readJSON(req)));
      } else if (parts.length === 3 && req.method === 'GET') {
        json(res, 200, authorizations.status(parts[2], owner));
      } else if (parts.length === 3 && req.method === 'DELETE') {
        authorizations.cancel(parts[2], owner);
        json(res, 200, { cancelled: true });
      } else if (parts.length === 4 && parts[3] === 'claim' && req.method === 'POST') {
        json(res, 200, authorizations.claim(parts[2], owner));
      } else if (parts.length === 4 && parts[3] === 'settle' && req.method === 'POST') {
        const body = await readJSON(req);
        if (typeof body.success !== 'boolean') throw failure(400, 'success must be boolean');
        authorizations.settle(parts[2], owner, body.claim, body.success);
        json(res, 200, { completed: body.success });
      } else { throw failure(404, 'Not found'); }
    } catch (error) {
      json(res, error.status || 502, { error: { message: error.status ? error.message : 'Cursor authorization failed' } });
    }
    return true;
  };
}
