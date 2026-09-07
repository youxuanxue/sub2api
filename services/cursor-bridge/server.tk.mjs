import { CursorAuthorizations, createInternalGuard } from './auth.tk.mjs';
import { createCatalogValidator } from './catalog.tk.mjs';
import { estimateParkedTurn } from './usage.tk.mjs';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

process.env.CURSOR_AGENT_EMBEDDED = '1';
const secret = process.env.CURSOR_BRIDGE_SECRET;
if (!secret || Buffer.byteLength(secret) < 32) throw new Error('CURSOR_BRIDGE_SECRET must contain at least 32 bytes');
if (process.env.CURSOR_AGENT_STATE_DIR) throw new Error('Persistent run recovery is not enabled for TokenKey');
process.env.CURSOR_AGENT_ALLOW_ENV_KEY = '0';
const { server, setRequestGuard, setMessageValidator, setUsageEstimator, setSdkStore, Cursor, JsonlLocalAgentStore } = await import('./upstream/server.mjs');
// Always override the SDK's persistent home store, including text-only runs.
const storeDirectory = mkdtempSync(join(tmpdir(), 'tokenkey-cursor-'));
setSdkStore(new JsonlLocalAgentStore(storeDirectory));
process.once('exit', () => rmSync(storeDirectory, { recursive: true, force: true }));
const authorizations = new CursorAuthorizations(Cursor);
setRequestGuard(createInternalGuard({ secret, authorizations }));
setMessageValidator(createCatalogValidator(Cursor));
setUsageEstimator(estimateParkedTurn);
server.on('close', () => authorizations.close());
server.listen(Number(process.env.CURSOR_AGENT_SIDECAR_PORT || 3927), process.env.CURSOR_AGENT_SIDECAR_HOST || '127.0.0.1', () => {
  console.log('TokenKey Cursor bridge is listening');
});
