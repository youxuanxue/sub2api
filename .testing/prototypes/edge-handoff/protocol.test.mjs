import { test } from 'node:test'
import assert from 'node:assert/strict'
import { generateKeyPairSync } from 'node:crypto'
import { createEdge, delegate, digest, random, ttl } from './protocol.mjs'
function fixture() {
  const { privateKey, publicKey } = generateKeyPairSync('ed25519')
  let time = 1000
  const verifier = random(), attempt = random()
  const claims = { iss: 'https://prod.test', origin: 'https://prod.test', aud: 'https://edge.test', purpose: 'edge-handoff', sub: 'admin-fixture', challenge: digest(verifier), attempt, exp: time + ttl }
  const edge = createEdge(publicKey, claims.aud, claims.origin, () => time)
  return { edge, verifier, attempt, claims, privateKey, grant: () => delegate(privateKey, claims), expire: () => { time += ttl } }
}
test('one delegation creates one code; invalid proof cannot consume it', () => {
  const f = fixture(), grant = f.grant(), row = f.edge.mint(grant)
  assert.throws(() => f.edge.mint(grant), /replayed/)
  assert.throws(() => f.edge.exchange({ ...row, verifier: random() }), /invalid/)
  assert.throws(() => f.edge.exchange({ ...row, verifier: f.verifier, attempt: random() }), /invalid/)
  assert.throws(() => f.edge.exchange({ ...row, verifier: f.verifier }, false), /invalid/)
  const result = f.edge.exchange({ ...row, verifier: f.verifier })
  assert.equal(result.initiator, 'admin-fixture')
  assert.ok(result.access_token && result.refresh_token)
  assert.throws(() => f.edge.exchange({ ...row, verifier: f.verifier }), /invalid/)
})
test('expired codes and cross-edge or tampered delegations are rejected', () => {
  const f = fixture(), row = f.edge.mint(f.grant())
  f.expire()
  assert.throws(() => f.edge.exchange({ ...row, verifier: f.verifier }), /invalid/)
  const other = fixture()
  assert.throws(() => f.edge.mint(other.grant()), /unauthorized/)
  const wrongAudience = delegate(other.privateKey, { ...other.claims, aud: 'https://wrong.test' })
  assert.throws(() => other.edge.mint(wrongAudience), /invalid/)
  assert.throws(() => other.edge.mint({ ...other.grant(), payload: 'tampered' }), /unauthorized/)
})
test('racing valid exchanges have exactly one winner', async () => {
  const f = fixture(), row = f.edge.mint(f.grant())
  const results = await Promise.allSettled(Array.from({ length: 8 }, () => Promise.resolve().then(() => f.edge.exchange({ ...row, verifier: f.verifier }))))
  assert.equal(results.filter(result => result.status === 'fulfilled').length, 1)
})
