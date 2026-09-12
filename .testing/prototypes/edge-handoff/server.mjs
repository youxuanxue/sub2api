import { createServer } from 'node:http'
import { generateKeyPairSync } from 'node:crypto'
import { readFile } from 'node:fs/promises'
import { createEdge, delegate, ttl } from './protocol.mjs'
const prod = 'http://127.0.0.1:4311', edgeOrigin = 'http://127.0.0.1:4312'
const { privateKey, publicKey } = generateKeyPairSync('ed25519')
const edge = createEdge(publicKey, edgeOrigin, prod)
const browser = await readFile(new URL('./browser.mjs', import.meta.url))
const style = '[hidden]{display:none!important}body{font:16px system-ui;background:#f8fafc;color:#172027;margin:0;display:grid;place-items:center;min-height:100vh}main{max-width:480px;padding:40px;background:white;border-radius:20px;box-shadow:0 12px 40px #0001}h1{font-size:28px}p{line-height:1.7;color:#52606d}button,a{display:inline-block;padding:12px 18px;border:0;border-radius:9px;font:inherit}button{background:#087f75;color:white;cursor:pointer}a{color:#087f75}small{display:block;color:#718096;margin-bottom:20px}'
function html(isProd) { return `<!doctype html><html lang="zh"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Edge 交接原型</title><style>${style}</style><main><small>本地协议原型 · 模拟管理员</small><h1>${isProd ? '管理你的 Edge' : '正在连接 Edge'}</h1><p id="status">${isProd ? '进入后即可继续管理账号。' : '正在安全地建立管理会话…'}</p><button>进入 Edge</button><a href="${edgeOrigin}/login">直接登录 Edge</a></main><script type="module" src="/browser.mjs"></script></html>` }
for (const port of [4311, 4312]) {
  const isProd = port === 4311, ownOrigin = isProd ? prod : edgeOrigin
  const server = createServer(async (req, res) => {
    res.setHeader('Cache-Control', 'no-store')
    res.setHeader('Referrer-Policy', 'no-referrer')
    res.setHeader('X-Content-Type-Options', 'nosniff')
    try {
      if (req.method === 'GET' && req.url === '/browser.mjs') { res.setHeader('Content-Type','text/javascript'); return res.end(browser) }
      if (req.method === 'GET' && req.url === '/login') { res.setHeader('Content-Type','text/html; charset=utf-8'); return res.end('<h1>Edge 登录</h1><p>原型不连接真实账号；生产沿用现有登录页。</p>') }
      if (req.method === 'GET' && (req.url === '/' || req.url === '/admin/edge-handoff')) { res.setHeader('Content-Type','text/html; charset=utf-8'); return res.end(html(isProd)) }
      if (req.method !== 'POST' || req.headers.origin !== ownOrigin) throw Error('origin rejected')
      let raw = ''
      for await (const chunk of req) { raw += chunk; if (raw.length > 4096) throw Error('too large') }
      const body = JSON.parse(raw)
      let result
      if (isProd && req.url === '/api/handoff') {
        // LOCAL FIXTURE ONLY. Production must verify the current admin JWT and
        // resolve a pinned Edge target before signing the delegation.
        const grant = delegate(privateKey, { iss: prod, origin: prod, aud: edgeOrigin, purpose: 'edge-handoff', sub: 'fixture-admin', challenge: body.challenge, attempt: body.attempt, exp: Date.now() + ttl })
        result = edge.mint(grant)
      } else if (!isProd && req.url === '/api/exchange') result = edge.exchange(body)
      else throw Error('unknown route')
      res.setHeader('Content-Type','application/json'); res.end(JSON.stringify(result))
    } catch { res.statusCode = 403; res.setHeader('Content-Type','application/json'); res.end('{"error":"handoff_failed"}') }
  })
  server.listen(port, '127.0.0.1')
}
console.log('Edge handoff prototype: http://127.0.0.1:4311')
