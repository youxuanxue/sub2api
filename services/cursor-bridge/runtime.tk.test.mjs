import test from 'node:test';
import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';

test('production entrypoint bounds CLI capacity without requiring Docker defaults', async () => {
  const { stdout } = await promisify(execFile)(process.execPath, ['--input-type=module', '-e', `
    await import('./server.tk.mjs');
    const { server } = await import('./upstream/server.mjs');
    if (!server.listening) await new Promise(resolve => server.once('listening', resolve));
    const response = await fetch('http://127.0.0.1:' + server.address().port + '/health');
    if (!response.ok || !(await response.json()).accepting) throw new Error('Not healthy');
    console.log('capacity=' + JSON.stringify({
      total: process.env.CURSOR_AGENT_MAX_ACTIVE_SESSIONS,
      credential: process.env.CURSOR_AGENT_MAX_SESSIONS_PER_CREDENTIAL,
    }));
    await new Promise(resolve => server.close(resolve));
  `], {
    cwd: new URL('.', import.meta.url), timeout: 15_000,
    env: { ...process.env, CURSOR_BRIDGE_SECRET: 's'.repeat(32),
      CURSOR_AGENT_STATE_DIR: '', CURSOR_AGENT_SIDECAR_PORT: '0',
      CURSOR_AGENT_SIDECAR_HOST: '127.0.0.1', CURSOR_AGENT_MAX_ACTIVE_SESSIONS: '',
      CURSOR_AGENT_MAX_SESSIONS_PER_CREDENTIAL: '',
    },
  });
  const capacity = JSON.parse(stdout.split('\n').find(line => line.startsWith('capacity=')).slice('capacity='.length));
  assert.deepEqual(capacity, { total: '4', credential: '4' });
});
