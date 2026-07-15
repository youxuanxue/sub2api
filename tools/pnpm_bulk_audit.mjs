#!/usr/bin/env node

import { spawnSync } from 'node:child_process'

const auditEndpoint = 'https://registry.npmjs.org/-/npm/v1/security/advisories/bulk'

export function collectPackageVersions(roots) {
  const versions = new Map()

  function visit(node, packageName) {
    if (!node || typeof node !== 'object') return
    if (packageName && node.version) {
      if (!versions.has(packageName)) versions.set(packageName, new Set())
      versions.get(packageName).add(node.version)
    }
    for (const [name, dependency] of Object.entries(node.dependencies ?? {})) {
      visit(dependency, name)
    }
  }

  for (const root of roots) {
    for (const [name, dependency] of Object.entries(root.dependencies ?? {})) {
      visit(dependency, name)
    }
  }

  return Object.fromEntries(
    [...versions.entries()].map(([name, values]) => [name, [...values].sort()])
  )
}

export function toLegacyAuditResult(bulkResult, dependencyCount) {
  const advisories = {}
  for (const [packageName, packageAdvisories] of Object.entries(bulkResult)) {
    for (const advisory of packageAdvisories) {
      const ghsa = advisory.url?.match(/GHSA-[a-z0-9-]+/i)?.[0]?.toUpperCase()
      advisories[`${packageName}:${advisory.id}`] = {
        ...advisory,
        module_name: packageName,
        ...(ghsa ? { github_advisory_id: ghsa } : {})
      }
    }
  }
  return { advisories, metadata: { dependencies: dependencyCount } }
}

async function main() {
  const listed = spawnSync('pnpm', ['list', '--prod', '--json', '--depth', 'Infinity'], {
    encoding: 'utf8',
    maxBuffer: 64 * 1024 * 1024
  })
  if (listed.status !== 0) {
    throw new Error(`pnpm list failed: ${listed.stderr.trim() || `exit ${listed.status}`}`)
  }

  const packageVersions = collectPackageVersions(JSON.parse(listed.stdout))
  if (Object.keys(packageVersions).length === 0) {
    throw new Error('pnpm list returned no production dependencies')
  }

  const response = await fetch(auditEndpoint, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify(packageVersions)
  })
  if (!response.ok) {
    throw new Error(`bulk advisory endpoint returned HTTP ${response.status}: ${await response.text()}`)
  }

  const result = toLegacyAuditResult(await response.json(), Object.keys(packageVersions).length)
  process.stdout.write(`${JSON.stringify(result, null, 2)}\n`)
}

main().catch((error) => {
  process.stderr.write(`pnpm bulk audit failed: ${error.message}\n`)
  process.exitCode = 1
})
