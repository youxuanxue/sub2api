import { readFileSync, writeFileSync, mkdirSync } from 'node:fs';
import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { resolve, dirname } from 'node:path';
import assert from 'node:assert/strict';

const root = fileURLToPath(new URL('../../', import.meta.url));
const evidence = name => JSON.parse(readFileSync(resolve(root, `.cache/cursor-dev/evidence/${name}.json`), 'utf8'));
const catalogPath = resolve(root, 'backend/internal/service/tk_served_models.json');
const catalog = JSON.parse(readFileSync(catalogPath, 'utf8'));
const models = Object.entries(catalog.entries).filter(([, row]) => row.scopes?.some(s => s.channel_type === 14 && s.base_url === 'http://cursor-bridge:3927')).map(([id]) => id).sort();
const chat = evidence('chat-all');
assert.deepEqual(chat.results.map(r => r.model).sort(), models, 'Every fixed catalog model must have a real UI result');
assert.equal(chat.denominator, models.length);
const tools = evidence('client-tools');
const billing = evidence('billing');
const parallel = evidence('client-parallel');
const container = evidence('container');
const authorization = evidence('authorization');
const claudeCode = evidence('claude-code');
assert.equal(authorization.modelCount, models.length);
assert.equal(billing.status, 'passed');
assert.equal(container.status, 'passed');
assert.deepEqual(tools.results.map(r => r.protocol), ['messages', 'chat', 'responses']);
assert.ok(tools.results.every(r => r.status === 'passed'));
assert.ok(parallel.results.every(r => r.status === 'passed'));
assert.equal(claudeCode.exitCode, 0);
assert.equal(claudeCode.result.is_error, false);
assert.ok(claudeCode.result.result.includes('CURSOR_CODE_VERIFIED_4736'));
// Public gateway errors are intentionally generic. Correlate internal provider
// diagnostics by the exact UI response request IDs without exporting payloads.
const requestIDs = chat.results.map(r => r.requestId);
assert.ok(requestIDs.every(id => /^[a-f0-9-]{36}$/.test(id)));
const sql = `SELECT COALESCE(json_agg(t),'[]') FROM (SELECT request_id,upstream_error_message FROM ops_error_logs WHERE request_id IN (${requestIDs.map(id => `'${id}'`).join(',')})) t`;
const diagnostics = JSON.parse(execFileSync('docker', ['exec', 'tokenkey-cursor-dev-postgres-1', 'psql', '-U', 'cursor_dev', '-d', 'cursor_dev', '-At', '-c', sql], { encoding: 'utf8' }));
const regionBlocked = id => diagnostics.some(d => d.request_id === id && /not supported in your region/i.test(d.upstream_error_message));
const results = chat.results.map(r => ({ model: r.model, status: r.status === 'passed' ? 'passed' : regionBlocked(r.requestId) ? 'blocked_region' : 'failed',
  httpStatus: r.httpStatus, elapsedMs: r.elapsedMs }));
const report = {
  recordedAt: new Date().toISOString(), environment: 'isolated local TokenKey; real Cursor subscription',
  newapiCommit: readFileSync(resolve(root, '.new-api-ref'), 'utf8').trim(),
  sidecarCommit: '9ab86e3bc0ca368df585edcc096ec172263d222d', sdkVersion: '1.0.31',
  catalogSHA256: createHash('sha256').update(readFileSync(catalogPath)).digest('hex'),
  authorization: { status: 'passed', method: authorization.via, modelCount: authorization.modelCount },
  chat: { at: chat.at, denominator: models.length, passed: results.filter(r => r.status === 'passed').length,
    blocked: results.filter(r => r.status === 'blocked_region').length, results },
  clientTools: tools.results.map(r => ({ protocol: r.protocol, model: r.model, status: r.status, turns: r.turns })),
  parallelTools: parallel.results.map(r => ({ model: r.model, protocol: r.protocol, tools: r.tools, status: r.status })),
  claudeCode: { status: 'passed', at: claudeCode.at, model: 'composer-2.5', tool: 'Read', exitCode: claudeCode.exitCode },
  billing: { status: billing.status, acceptedTurns: billing.rows.length, replayRejectedWithoutCharge: billing.replayRejectedWithoutCharge,
    pricingSource: 'backend/internal/service/tk_pricing_overlay.json', policy: 'cursor-sdk-estimated' },
  container: { architecture: container.architecture, node: container.node, status: container.status, textStateCleanup: container.textStateCleanup },
  overall: results.every(r => r.status === 'passed') ? 'chat_and_client_checks_passed' : 'incomplete_all_model_acceptance',
  limitations: [
    'Claude, GPT and Gemini inference remains blocked by Cursor regional policy on the tested account/egress.',
    'Cursor estimates missing parked-tool usage; negative final deltas do not produce refunds.',
    'Client max_tokens is not an SDK upstream spending cap.',
    'Single bridge/account topology; live distributed Prod-to-Edge deployment is not verified.',
  ],
};
const output = resolve(root, process.argv[2] || 'services/cursor-bridge/validation/2026-09-07.json');
mkdirSync(dirname(output), { recursive: true });
writeFileSync(output, JSON.stringify(report, null, 2) + '\n');
console.log(`Recorded ${models.length} real UI results: ${report.chat.passed} passed, ${report.chat.blocked} region blocked`);
