import { mkdirSync, existsSync, readFileSync, writeFileSync, openSync, closeSync } from 'node:fs';
import { randomBytes } from 'node:crypto';
import { spawn, spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { resolve } from 'node:path';

const root = fileURLToPath(new URL('../../', import.meta.url));
const state = resolve(root, '.cache/cursor-dev');
mkdirSync(state, { recursive: true, mode: 0o700 });
const envFile = resolve(state, 'env.json');
if (!existsSync(envFile)) {
  const secret = () => randomBytes(32).toString('hex');
  writeFileSync(envFile, JSON.stringify({
    DATABASE_HOST: '127.0.0.1', DATABASE_PORT: '15439', DATABASE_USER: 'cursor_dev', DATABASE_DBNAME: 'cursor_dev',
    DATABASE_PASSWORD: secret(), DATABASE_SSLMODE: 'disable',
    REDIS_HOST: '127.0.0.1', REDIS_PORT: '16389', REDIS_DB: '0',
    AUTO_SETUP: 'true', ADMIN_EMAIL: 'admin@cursor.local', ADMIN_PASSWORD: secret(),
    JWT_SECRET: secret(), TOTP_ENCRYPTION_KEY: secret(),
    SERVER_HOST: '127.0.0.1', SERVER_PORT: '18097', SERVER_MODE: 'release', DATA_DIR: resolve(state, 'app'),
    VITE_DEV_PROXY_TARGET: 'http://127.0.0.1:18097', VITE_DEV_PORT: '15179'
  }, null, 2), { mode: 0o600 });
}
const env = { ...process.env, TZ: 'Asia/Shanghai', ...JSON.parse(readFileSync(envFile, 'utf8')) };
mkdirSync(env.DATA_DIR, { recursive: true, mode: 0o700 });
function run(command, args, cwd = root) {
  const result = spawnSync(command, args, { cwd, env, stdio: 'inherit' });
  if (result.status !== 0) process.exit(result.status || 1);
}
async function launch(name, command, args, cwd, health) {
  if (await fetch(health).then(r => r.ok).catch(() => false)) throw new Error(`${name} port is occupied; stop this test stack before restarting`);
  const log = openSync(resolve(state, `${name}.log`), 'a', 0o600);
  const child = spawn(command, args, { cwd, env, detached: true, stdio: ['ignore', log, log] });
  child.unref();
  closeSync(log);
  writeFileSync(resolve(state, `${name}.pid`), String(child.pid), { mode: 0o600 });
  console.log(`Started ${name}; log: ${resolve(state, `${name}.log`)}`);
  for (let attempt = 0; attempt < 120; attempt++) {
    if (await fetch(health).then(r => r.ok).catch(() => false)) return;
    if (child.exitCode !== null) throw new Error(`${name} exited before becoming healthy`);
    await new Promise(resolve => setTimeout(resolve, 500));
  }
  throw new Error(`${name} did not become healthy within 60 seconds`);
}
const action = process.argv[2];
if (action === 'prepare') {
  run('docker', ['compose', '-f', 'deploy/cursor/local.compose.yml', 'up', '-d', '--wait']);
  run('go', ['build', '-o', resolve(state, 'tokenkey'), './cmd/server'], resolve(root, 'backend'));
} else if (action === 'start') {
  await launch('backend', resolve(state, 'tokenkey'), [], resolve(root, 'backend'), 'http://127.0.0.1:18097/health');
  await launch('frontend', 'pnpm', ['dev', '--host', '127.0.0.1', '--strictPort'], resolve(root, 'frontend'), 'http://127.0.0.1:15179');
} else if (action === 'stop') {
  for (const name of ['frontend', 'backend']) {
    const file = resolve(state, `${name}.pid`);
    if (!existsSync(file)) continue;
    const pid = Number(readFileSync(file, 'utf8'));
    const result = spawnSync('ps', ['-p', String(pid), '-o', 'command='], { encoding: 'utf8' });
    const expected = name === 'backend' ? resolve(state, 'tokenkey') : 'pnpm';
    if (result.stdout.includes(expected)) {
      try { process.kill(-pid, 'SIGTERM'); } catch (error) { if (error.code !== 'ESRCH') throw error; }
      for (let attempt = 0; attempt < 80; attempt++) {
        try { process.kill(pid, 0); } catch (error) { if (error.code === 'ESRCH') break; throw error; }
        if (attempt === 79) throw new Error(`${name} did not finish draining within 40 seconds`);
        await new Promise(resolve => setTimeout(resolve, 500));
      }
    }
  }
} else throw new Error('Usage: node scripts/cursor/local-dev.mjs prepare|start|stop');
