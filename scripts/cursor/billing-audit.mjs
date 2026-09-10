import { readFileSync, writeFileSync } from 'node:fs';
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { resolve } from 'node:path';
import assert from 'node:assert/strict';
const root = fileURLToPath(new URL('../../', import.meta.url));
const directory = resolve(root, '.cache/cursor-dev/evidence');
const mode = process.argv[2] || 'tools';
assert.ok(['tools', 'parallel', 'text'].includes(mode), 'Unknown probe mode');
const probe = JSON.parse(readFileSync(resolve(directory, `client-${mode}.json`), 'utf8'));
const ids = probe.requests.map(request => request.clientRequestId);
assert.ok(ids.length > 0, 'No requests to audit');
assert.equal(new Set(ids).size, ids.length, 'Request IDs must be unique');
assert.ok(ids.every(id => /^[a-f0-9-]{36}$/.test(id)), 'Run the client probe with request correlation first');
const sql = `SELECT COALESCE(json_agg(t),'[]') FROM (SELECT request_id,input_tokens,output_tokens,cache_read_tokens,cache_creation_tokens,total_cost,actual_cost,billing_tier FROM usage_logs WHERE request_id IN (${ids.map(id => `'client:${id}'`).join(',')})) t`;
const rows = JSON.parse(execFileSync('docker', ['exec', 'tokenkey-cursor-dev-postgres-1', 'psql', '-U', 'cursor_dev', '-d', 'cursor_dev', '-At', '-c', sql], { encoding: 'utf8' }));
const prices = JSON.parse(readFileSync(resolve(root, 'backend/internal/service/tk_pricing_overlay.json'), 'utf8'));
const results = [];
const audited = new Set();
for (const result of probe.results) {
  assert.equal(result.status, 'passed');
  const pricing = prices[result.model];
  assert.ok(pricing, `Missing pricing for ${result.model}`);
  assert.equal(result.turns.length, result.requestIds.length);
  for (const [index, usage] of result.turns.entries()) {
    const requestId = result.requestIds[index];
    const request = probe.requests.find(item => item.clientRequestId === requestId);
    assert.equal(request?.status, 200);
    assert.ok(!audited.has(requestId), 'A request may only be audited once');
    audited.add(requestId);
    const matching = rows.filter(row => row.request_id === `client:${request.clientRequestId}`);
    assert.equal(matching.length, 1, 'Each accepted request must have one usage record');
    const row = matching[0];
    assert.ok(['cursor-oauth-reported', 'cursor-oauth-estimated'].includes(row.billing_tier), 'Missing native usage provenance');
    if (usage.tk_billing_tier) assert.equal(row.billing_tier, usage.tk_billing_tier);
    const cached = usage.cache_read_input_tokens || usage.prompt_tokens_details?.cached_tokens || usage.input_tokens_details?.cached_tokens || 0;
    const cacheCreated = usage.cache_creation_input_tokens || usage.prompt_tokens_details?.cache_creation_tokens || 0;
    const output = usage.output_tokens ?? usage.completion_tokens;
    const input = result.protocol === 'messages' ? usage.input_tokens : (usage.prompt_tokens ?? usage.input_tokens) - cached - cacheCreated;
    assert.equal(row.input_tokens, input);
    assert.equal(row.output_tokens, output);
    assert.equal(row.cache_read_tokens, cached);
    assert.equal(row.cache_creation_tokens, cacheCreated);
    if (row.billing_tier === 'cursor-oauth-estimated') {
      assert.equal(cached, 0, 'Missing-usage estimate must not invent cache hits');
      assert.equal(cacheCreated, 0, 'Missing-usage estimate must not invent cache writes');
    }
    const cost = input * pricing.input_cost_per_token + output * pricing.output_cost_per_token + cached * (pricing.cache_read_input_token_cost || 0) + cacheCreated * (pricing.cache_creation_input_token_cost || 0);
    assert.ok(Math.abs(row.actual_cost - cost) < 1e-9);
    results.push({ model: result.model, protocol: result.protocol, requestId: request.clientRequestId, tier: row.billing_tier, input, output, cached, cacheCreated, cost: row.actual_cost });
  }
}
for (const request of probe.requests) {
  if (request.status === 200) assert.ok(audited.has(request.clientRequestId), 'Accepted request missing from billing audit');
  else assert.ok(!rows.some(row => row.request_id === `client:${request.clientRequestId}`));
}
writeFileSync(resolve(directory, `billing-${mode}.json`), JSON.stringify({ at: new Date().toISOString(), status: 'passed', rows: results }, null, 2));
console.log(`Billing matched all ${results.length} accepted turns, including any full-history replay`);
