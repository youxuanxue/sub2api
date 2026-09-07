import { readFileSync, writeFileSync } from 'node:fs';
import { execFileSync, spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { resolve } from 'node:path';
import assert from 'node:assert/strict';

const root = fileURLToPath(new URL('../../', import.meta.url));
const state = resolve(root, '.cache/cursor-dev');
const config = JSON.parse(readFileSync(resolve(state, 'env.json'), 'utf8'));
const docker = args => execFileSync('docker', args, { encoding: 'utf8', env: { ...process.env, CURSOR_BRIDGE_SECRET: config.CURSOR_BRIDGE_SECRET }, stdio: ['ignore', 'pipe', 'pipe'] });
const account = JSON.parse(docker(['exec', 'tokenkey-cursor-dev-postgres-1', 'psql', '-U', 'cursor_dev', '-d', 'cursor_dev', '-At', '-c', "SELECT credentials FROM accounts WHERE extra->>'upstream_provider'='cursor' AND deleted_at IS NULL ORDER BY id LIMIT 1"]));
const name = `tokenkey-cursor-probe-${process.pid}`;
const base = 'http://127.0.0.1:13927';
const model = 'composer-2.5';
const params = account.cursor_model_parameters[model];
const headers = { 'Content-Type': 'application/json', 'X-TokenKey-Bridge-Secret': config.CURSOR_BRIDGE_SECRET,
  'X-Cursor-Agent-Tenant': 'probe:container', 'X-Api-Key': account.api_key };
async function message(messages, tools = []) {
  const response = await fetch(`${base}/v1/messages`, { method: 'POST', headers, signal: AbortSignal.timeout(90_000),
    body: JSON.stringify({ model, cursor_model: { id: model, params }, max_tokens: 128, messages, tools }) });
  assert.equal(response.status, 200, `Container inference HTTP ${response.status}`);
  return response.json();
}
try {
  docker(['run', '-d', '--name', name, '--init', '--read-only', '--cap-drop=ALL', '--security-opt=no-new-privileges:true',
    '--memory=2g', '--tmpfs', '/tmp:rw,noexec,nosuid,size=512m,mode=1777', '--tmpfs', '/home/node:rw,nosuid,size=128m,uid=1000,gid=1000',
    '-e', 'CURSOR_BRIDGE_SECRET', '-p', '127.0.0.1:13927:3927', 'tokenkey-cursor-bridge:dev']);
  let healthy = false;
  for (let attempt = 0; attempt < 60; attempt++) {
    if (await fetch(`${base}/health`).then(r => r.ok).catch(() => false)) { healthy = true; break; }
    await new Promise(resolve => setTimeout(resolve, 500));
  }
  assert.ok(healthy, 'Container did not become healthy');
  assert.equal((await fetch(`${base}/v1/models`)).status, 401);
  const text = await message([{ role: 'user', content: 'Reply exactly OK.' }]);
  assert.match(text.content.filter(c => c.type === 'text').map(c => c.text).join('').trim(), /^OK[.!]?$/i);
  const countStore = "const fs=require('fs');const dirs=fs.readdirSync('/tmp').filter(n=>n.startsWith('tokenkey-cursor-'));let rows=0;for(const d of dirs)for(const f of fs.readdirSync('/tmp/'+d))if(f.endsWith('.ndjson'))rows+=fs.readFileSync('/tmp/'+d+'/'+f,'utf8').split('\\n').filter(Boolean).length;console.log(rows)";
  assert.equal(Number(docker(['exec', name, 'node', '-e', countStore]).trim()), 0, 'Text run SDK state was not deleted');
  const tools = [{ name: 'read_fixture', description: 'Read the verification fixture.', input_schema: { type: 'object', properties: {}, additionalProperties: false } }];
  const messages = [{ role: 'user', content: 'Call read_fixture, then return its exact contents.' }];
  const first = await message(messages, tools);
  const calls = first.content.filter(c => c.type === 'tool_use');
  assert.equal(calls.length, 1);
  assert.equal(calls[0].name, 'read_fixture');
  messages.push({ role: 'assistant', content: first.content }, { role: 'user', content: [{ type: 'tool_result', tool_use_id: calls[0].id, content: 'CURSOR_CONTAINER_VERIFIED_4736' }] });
  const final = await message(messages, tools);
  assert.ok(final.content.some(c => c.type === 'text' && c.text.includes('CURSOR_CONTAINER_VERIFIED_4736')));
  const architecture = docker(['image', 'inspect', 'tokenkey-cursor-bridge:dev', '--format', '{{.Architecture}}']).trim();
  writeFileSync(resolve(state, 'evidence/container.json'), JSON.stringify({ at: new Date().toISOString(), status: 'passed', architecture,
    node: docker(['exec', name, 'node', '--version']).trim(), model, nonRoot: true, readOnly: true, tmpNoExec: true,
    text: 'passed', textStateCleanup: 'passed', tools: 'passed', unauthorized: 401 }, null, 2));
  console.log(`Container text, tools, SDK state cleanup and authentication passed (${architecture})`);
} catch (error) {
  console.error(error.message);
  process.exitCode = 1;
} finally {
  // Only remove the uniquely named container created by this invocation.
  spawnSync('docker', ['stop', '--time', '35', name], { stdio: 'ignore' });
  spawnSync('docker', ['rm', name], { stdio: 'ignore' });
}
