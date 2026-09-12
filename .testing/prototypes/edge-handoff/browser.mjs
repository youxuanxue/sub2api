const prod = 'http://127.0.0.1:4311', edge = 'http://127.0.0.1:4312'
const status = document.querySelector('#status')
const button = document.querySelector('button')
const random = () => btoa(String.fromCharCode(...crypto.getRandomValues(new Uint8Array(32)))).replaceAll('+', '-').replaceAll('/', '_').replaceAll('=', '')
const digest = async value => btoa(String.fromCharCode(...new Uint8Array(await crypto.subtle.digest('SHA-256', new TextEncoder().encode(value))))).replaceAll('+', '-').replaceAll('/', '_').replaceAll('=', '')
async function post(path, body) {
  const response = await fetch(path, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body), signal: AbortSignal.timeout(10_000) })
  if (!response.ok) throw Error('handoff unavailable')
  return response.json()
}
if (location.origin === prod) {
  button.onclick = () => {
    button.disabled = true
    const child = window.open(edge + '/admin/edge-handoff', '_blank')
    if (!child) { status.textContent = '请允许弹出窗口后重试，或直接登录 Edge。'; button.disabled = false; return }
    let received = false, active = true
    const cleanup = () => { active = false; window.removeEventListener('message', receive); clearTimeout(timer); button.disabled = false }
    const timer = setTimeout(() => { cleanup(); status.textContent = '连接超时。可以重试，或直接登录 Edge。' }, 15_000)
    async function receive(event) {
      if (event.origin !== edge || event.source !== child || event.data?.type !== 'edge-ready' || received) return
      received = true
      try {
        const result = await post('/api/handoff', { challenge: event.data.challenge, attempt: event.data.attempt })
        if (!active) return
        if (child.closed) throw Error('closed')
        child.postMessage({ type: 'edge-code', ...result }, edge)
        status.textContent = '已交接，请在 Edge 窗口继续。'
      } catch { status.textContent = '暂时无法连接。可以重试，或直接登录 Edge。' }
      finally { cleanup() }
    }
    window.addEventListener('message', receive)
  }
} else {
  button.hidden = true
  const parent = window.opener
  if (!parent || location.search || location.hash) status.textContent = '请从管理台重新打开，或直接登录。'
  else {
    let verifier = random(), used = false, active = true
    const attempt = random(), challenge = await digest(verifier)
    const timer = setTimeout(() => { cleanup(); status.textContent = '交接已过期，请重新打开或直接登录。' }, 15_000)
    const cleanup = () => { window.removeEventListener('message', receive); clearTimeout(timer); verifier = ''; active = false }
    async function receive(event) {
      if (used || event.origin !== prod || event.source !== parent || event.data?.type !== 'edge-code' || event.data.attempt !== attempt) return
      used = true
      try {
        const session = await post('/api/exchange', { code: event.data.code, attempt, verifier })
        // The fixture proves credentials reach only this child. Production hands
        // the pair directly to this Edge's existing auth store here.
        if (!active) return
        if (!session.access_token || !session.refresh_token) throw Error('missing session')
        status.textContent = '已进入 Edge 管理台'
        document.querySelector('h1').textContent = 'Edge 管理台'
        window.opener = null
      } catch { status.textContent = '交接失败，请重新打开或直接登录。' }
      finally { cleanup() }
    }
    window.addEventListener('message', receive)
    parent.postMessage({ type: 'edge-ready', challenge, attempt }, prod)
  }
}
