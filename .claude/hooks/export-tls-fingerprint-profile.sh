#!/usr/bin/env bash
# Capture the local Claude Code CLI TLS fingerprint shape at session end and
# save TokenKey-compatible profile samples under .tls_list/.
set -u

REPO_ROOT="${CLAUDE_PROJECT_DIR:-$(cd "$(dirname "$0")/../.." && pwd)}"

if [ "${CLAUDE_CODE_REMOTE:-}" = "true" ]; then
  printf '{"suppressOutput":true}\n'
  exit 0
fi

if ! command -v node >/dev/null 2>&1; then
  printf '{"systemMessage":"TokenKey TLS profile capture skipped: node is not available","suppressOutput":false}\n'
  exit 0
fi

export TOKENKEY_TLS_CAPTURE_REPO_ROOT="$REPO_ROOT"
TOKENKEY_TLS_CAPTURE_HOOK_INPUT="$(cat || true)"
export TOKENKEY_TLS_CAPTURE_HOOK_INPUT

node <<'NODE'
const fs = require('fs')
const https = require('https')
const os = require('os')
const path = require('path')

const repoRoot = process.env.TOKENKEY_TLS_CAPTURE_REPO_ROOT || process.cwd()
const outDir = path.join(repoRoot, '.tls_list')
const captureUrl = process.env.TOKENKEY_TLS_PROFILE_CAPTURE_URL || 'https://tls.peet.ws/api/all'

function emit(obj) {
  process.stdout.write(JSON.stringify(obj) + '\n')
}

function requestJson(url) {
  return new Promise((resolve, reject) => {
    const req = https.request(url, {
      method: 'GET',
      timeout: 10000,
      headers: {
        'Accept': 'application/json',
        'User-Agent': `TokenKey-ClaudeCode-TLSProfileCollector/${process.version}`
      }
    }, (res) => {
      let body = ''
      res.setEncoding('utf8')
      res.on('data', chunk => { body += chunk })
      res.on('end', () => {
        if (res.statusCode < 200 || res.statusCode >= 300) {
          reject(new Error(`collector returned HTTP ${res.statusCode}`))
          return
        }
        try {
          resolve(JSON.parse(body))
        } catch (err) {
          reject(new Error(`collector returned non-JSON response: ${err.message}`))
        }
      })
    })
    req.on('timeout', () => req.destroy(new Error('collector request timed out')))
    req.on('error', reject)
    req.end()
  })
}

function parseNumList(value, separator = '-') {
  if (!value) return []
  return String(value)
    .split(separator)
    .map(s => s.trim())
    .filter(Boolean)
    .map(s => Number.parseInt(s, 10))
    .filter(Number.isFinite)
}

function parseJa3(ja3) {
  const parts = String(ja3 || '').split(',')
  return {
    cipher_suites: parseNumList(parts[1]),
    extensions: parseNumList(parts[2]),
    curves: parseNumList(parts[3]),
    point_formats: parseNumList(parts[4])
  }
}

function findExtension(tls, needle) {
  return (tls.extensions || []).find(ext => String(ext.name || '').includes(needle)) || null
}

function supportedVersionId(value) {
  const s = String(value || '').toUpperCase()
  if (s.includes('1.3')) return 772
  if (s.includes('1.2')) return 771
  if (s.includes('1.1')) return 770
  if (s.includes('1.0')) return 769
  const n = Number.parseInt(s, 10)
  return Number.isFinite(n) ? n : null
}

function parseSupportedVersions(tls) {
  const ext = findExtension(tls, 'supported_versions')
  const versions = ext && Array.isArray(ext.versions) ? ext.versions : []
  return versions.map(supportedVersionId).filter(v => v !== null)
}

function parseKeyShareGroups(tls) {
  const ext = findExtension(tls, 'key_share')
  const shares = ext && Array.isArray(ext.shared_keys) ? ext.shared_keys : []
  const groups = []
  for (const share of shares) {
    for (const key of Object.keys(share || {})) {
      const match = key.match(/\((\d+)\)/)
      if (match) groups.push(Number.parseInt(match[1], 10))
    }
  }
  return groups.filter(Number.isFinite)
}

function parsePskModes(tls) {
  const ext = findExtension(tls, 'psk_key_exchange_modes')
  if (!ext) return []
  const raw = String(ext.PSK_Key_Exchange_Mode || ext.data || '')
  const match = raw.match(/\((\d+)\)\s*$/)
  return match ? [Number.parseInt(match[1], 10)] : [1]
}

function parseAlpnProtocols(tls, payload) {
  const ext = findExtension(tls, 'application_layer_protocol_negotiation')
  if (ext && Array.isArray(ext.protocols) && ext.protocols.length > 0) {
    return ext.protocols.map(String)
  }
  if (payload.http_version === 'h2' || payload.http_version === 'HTTP/2') return ['h2']
  if (payload.http_version) return ['http/1.1']
  return []
}

function parseSignatureAlgorithms(tls) {
  const peetprint = String(tls.peetprint || '')
  const segments = peetprint.split('|')
  if (segments.length >= 4) {
    const parsed = parseNumList(segments[3])
    if (parsed.length > 0) return parsed
  }
  const ext = findExtension(tls, 'signature_algorithms')
  const values = ext && Array.isArray(ext.signature_algorithms) ? ext.signature_algorithms : []
  const byName = new Map([
    ['ecdsa_secp256r1_sha256', 0x0403],
    ['ecdsa_secp384r1_sha384', 0x0503],
    ['ecdsa_secp521r1_sha512', 0x0603],
    ['ed25519', 0x0807],
    ['ed448', 0x0808],
    ['rsa_pss_pss_sha256', 0x0809],
    ['rsa_pss_pss_sha384', 0x080a],
    ['rsa_pss_pss_sha512', 0x080b],
    ['rsa_pss_rsae_sha256', 0x0804],
    ['rsa_pss_rsae_sha384', 0x0805],
    ['rsa_pss_rsae_sha512', 0x0806],
    ['rsa_pkcs1_sha256', 0x0401],
    ['rsa_pkcs1_sha384', 0x0501],
    ['rsa_pkcs1_sha512', 0x0601],
    ['ecdsa_sha1', 0x0203],
    ['rsa_pkcs1_sha1', 0x0201],
    ['dsa_sha1', 0x0202],
    ['ecdsa_brainpoolP256r1tls13_sha256', 0x081a],
    ['ecdsa_brainpoolP384r1tls13_sha384', 0x081b],
    ['ecdsa_brainpoolP512r1tls13_sha512', 0x081c]
  ])
  return values.map(v => {
    const s = String(v)
    if (s.startsWith('0x')) return Number.parseInt(s.slice(2), 16)
    return byName.get(s) || null
  }).filter(v => v !== null && Number.isFinite(v))
}

function isGreaseValue(v) {
  return (v & 0x0f0f) === 0x0a0a && ((v >> 8) & 0xff) === (v & 0xff)
}

function buildYaml(profile) {
  const arr = value => `[${(value || []).map(v => typeof v === 'string' ? JSON.stringify(v) : String(v)).join(', ')}]`
  return [
    '# TokenKey TLS Fingerprint Profile sample captured by Claude Code SessionEnd hook',
    `${profile.name}:`,
    `  name: ${JSON.stringify(profile.name)}`,
    `  description: ${JSON.stringify(profile.description || '')}`,
    `  enable_grease: ${profile.enable_grease ? 'true' : 'false'}`,
    `  cipher_suites: ${arr(profile.cipher_suites)}`,
    `  curves: ${arr(profile.curves)}`,
    `  point_formats: ${arr(profile.point_formats)}`,
    `  signature_algorithms: ${arr(profile.signature_algorithms)}`,
    `  alpn_protocols: ${arr(profile.alpn_protocols)}`,
    `  supported_versions: ${arr(profile.supported_versions)}`,
    `  key_share_groups: ${arr(profile.key_share_groups)}`,
    `  psk_modes: ${arr(profile.psk_modes)}`,
    `  extensions: ${arr(profile.extensions)}`,
    ''
  ].join('\n')
}

function safeSessionId(hookInput) {
  const raw = String((hookInput && hookInput.session_id) || 'session')
  return raw.replace(/[^a-zA-Z0-9_-]/g, '').slice(0, 12) || 'session'
}

async function main() {
  let hookInput = {}
  const hookInputRaw = process.env.TOKENKEY_TLS_CAPTURE_HOOK_INPUT || ''
  if (hookInputRaw.trim()) {
    try { hookInput = JSON.parse(hookInputRaw) } catch {}
  }

  const payload = await requestJson(captureUrl)
  const tls = payload.tls || {}
  if (!tls.ja3) throw new Error('collector response missing tls.ja3')

  const ja3 = parseJa3(tls.ja3)
  const capturedAt = new Date().toISOString()
  const stamp = capturedAt.replace(/[-:]/g, '').replace(/\.\d{3}Z$/, 'Z')
  const ja3Hash = String(tls.ja3_hash || 'noja3hash')
  const shortJa3 = ja3Hash.replace(/[^a-fA-F0-9]/g, '').slice(0, 12) || 'noja3hash'
  const sessionShort = safeSessionId(hookInput)
  const name = `claude_code_cli_${stamp.slice(0, 8)}_${shortJa3}`.slice(0, 100)

  const profile = {
    name,
    description: [
      `Captured by Claude Code SessionEnd hook at ${capturedAt}.`,
      `runtime=node ${process.version} ${process.platform}/${process.arch}.`,
      `ja3_hash=${tls.ja3_hash || ''}.`,
      `ja4=${tls.ja4 || ''}.`,
      `source=${captureUrl}.`
    ].join(' '),
    enable_grease: [
      ...ja3.cipher_suites,
      ...ja3.extensions,
      ...ja3.curves
    ].some(isGreaseValue),
    cipher_suites: ja3.cipher_suites,
    curves: ja3.curves,
    point_formats: ja3.point_formats,
    signature_algorithms: parseSignatureAlgorithms(tls),
    alpn_protocols: parseAlpnProtocols(tls, payload),
    supported_versions: parseSupportedVersions(tls),
    key_share_groups: parseKeyShareGroups(tls),
    psk_modes: parsePskModes(tls),
    extensions: ja3.extensions
  }

  fs.mkdirSync(outDir, { recursive: true })
  const base = `${stamp}_${sessionShort}_${shortJa3}`
  const profilePath = path.join(outDir, `${base}.tokenkey-profile.json`)
  const yamlPath = path.join(outDir, `${base}.tokenkey-profile.yaml`)
  const capturePath = path.join(outDir, `${base}.capture.json`)

  fs.writeFileSync(profilePath, JSON.stringify(profile, null, 2) + '\n')
  fs.writeFileSync(yamlPath, buildYaml(profile))
  fs.writeFileSync(capturePath, JSON.stringify({
    schema_version: 1,
    captured_at: capturedAt,
    collector: {
      url: captureUrl,
      runtime: `node ${process.version}`,
      platform: process.platform,
      arch: process.arch,
      hostname: os.hostname()
    },
    claude_hook: {
      event: hookInput.hook_event_name || hookInput.event || 'SessionEnd',
      session_id: hookInput.session_id || null
    },
    observed: {
      http_version: payload.http_version || null,
      user_agent: payload.user_agent || null,
      ja3: tls.ja3 || null,
      ja3_hash: tls.ja3_hash || null,
      ja4: tls.ja4 || null,
      ja4_r: tls.ja4_r || null,
      peetprint: tls.peetprint || null,
      peetprint_hash: tls.peetprint_hash || null
    },
    tokenkey_profile: profile
  }, null, 2) + '\n')

  emit({
    systemMessage: `TokenKey TLS profile captured: ${path.relative(repoRoot, profilePath)}`,
    suppressOutput: true
  })
}

main().catch(err => {
  emit({
    systemMessage: `TokenKey TLS profile capture failed: ${err.message}`,
    suppressOutput: false
  })
  process.exit(0)
})
NODE
