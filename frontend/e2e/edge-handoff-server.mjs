import { spawn, spawnSync } from 'node:child_process'
import { mkdtempSync, rmSync, readFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
const backend = new URL('../../backend/', import.meta.url).pathname
const dir = mkdtempSync(join(tmpdir(), 'tk-edge-browser-'))
const bin = join(dir, 'server')
const toolchain = 'go' + readFileSync(join(backend, 'go.mod'), 'utf8').match(/^go ([^\s]+)/m)[1]
const built = spawnSync('go', ['build', '-tags=edge_handoff_e2e', '-o', bin, './cmd/edge-handoff-e2e'], { cwd: backend, stdio: 'inherit', env: { ...process.env, GOTOOLCHAIN: toolchain } })
if (built.status !== 0) { rmSync(dir, { recursive: true, force: true }); process.exit(built.status || 1) }
const redis = spawn('redis-server', ['--bind', '127.0.0.1', '--port', '4323', '--save', '', '--appendonly', 'no', '--dir', dir], { stdio: ['ignore', 'ignore', 'inherit'] })
let server
let stopping = false
function stop() {
  if (stopping) return
  stopping = true
  server?.kill('SIGTERM'); redis.kill('SIGTERM')
  rmSync(dir, { recursive: true, force: true })
}
process.on('SIGTERM', () => { stop(); process.exit() })
process.on('SIGINT', () => { stop(); process.exit() })
redis.on('exit', code => { if (!stopping) { stop(); process.exit(code || 1) } })
await new Promise(resolve => setTimeout(resolve, 300))
server = spawn(bin, [], { cwd: backend, stdio: 'inherit', env: { ...process.env, EDGE_HANDOFF_E2E_DIR: dir } })
server.on('exit', code => { if (!stopping) { stop(); process.exit(code || 1) } })
