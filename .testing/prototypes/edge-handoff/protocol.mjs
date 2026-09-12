import { createHash, randomBytes, sign, verify } from 'node:crypto'
export const ttl = 60_000
export const digest = value => createHash('sha256').update(value).digest('base64url')
export const random = () => randomBytes(32).toString('base64url')
export function delegate(privateKey, claims) {
  const payload = Buffer.from(JSON.stringify(claims)).toString('base64url')
  return { payload, signature: sign(null, Buffer.from(payload), privateKey).toString('base64url') }
}

// Protocol prototype only: the synchronous map section models the production
// Redis validation-and-consume operation. No persistent session or real identity.
export function createEdge(publicKey, audience, parentOrigin, now = Date.now) {
  const grants = new Map()
  const attempts = new Map()
  function mint(envelope) {
    if (!verify(null, Buffer.from(envelope.payload ?? ''), publicKey, Buffer.from(envelope.signature ?? '', 'base64url'))) throw Error('unauthorized')
    const claims = JSON.parse(Buffer.from(envelope.payload, 'base64url').toString())
    const time = now()
    if (claims.purpose !== 'edge-handoff' || claims.aud !== audience || claims.origin !== parentOrigin
      || claims.iss !== parentOrigin || typeof claims.sub !== 'string' || !claims.sub
      || !Number.isSafeInteger(claims.exp) || claims.exp <= time || claims.exp > time + ttl
      || !/^[A-Za-z0-9_-]{43}$/.test(claims.challenge ?? '') || !/^[A-Za-z0-9_-]{43}$/.test(claims.attempt ?? '')) throw Error('invalid delegation')
    for (const [key, row] of grants) if (row.exp <= time) grants.delete(key)
    for (const [key, expiry] of attempts) if (expiry <= time) attempts.delete(key)
    if (attempts.has(claims.attempt)) throw Error('replayed delegation')
    attempts.set(claims.attempt, claims.exp)
    const code = random()
    grants.set(digest(code), claims)
    return { code, attempt: claims.attempt }
  }
  function exchange({ code, verifier, attempt }, activeAdmin = true) {
    const key = digest(typeof code === 'string' ? code : '')
    const claims = grants.get(key)
    if (!claims || claims.exp <= now() || !activeAdmin || typeof verifier !== 'string'
      || !/^[A-Za-z0-9_-]{43}$/.test(verifier) || claims.attempt !== attempt || claims.challenge !== digest(verifier)) throw Error('invalid exchange')
    grants.delete(key)
    return { access_token: random(), refresh_token: random(), initiator: claims.sub }
  }
  return { mint, exchange }
}
