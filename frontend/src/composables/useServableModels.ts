import { reactive, ref } from 'vue'
import { getModelsListCandidates } from '@/api/admin/groups'
import { unknownToErrorMessage } from '@/utils/authError'
import type { GroupPlatform } from '@/types'

// TK (R-003, follow-up to PR #752): the self-healing servable model lists for the
// platforms whose candidates the backend authoritatively tracks (empirical
// allowlist + live model_availability pruning). The admin model-whitelist
// selector derives its candidates from here instead of hardcoded arrays, so an
// upstream-retired model (e.g. access-gated claude-fable-5) auto-drops without a
// manual frontend edit, and per-platform truth is honoured (gone on anthropic,
// still servable on antigravity). newapi (channel-driven) and the long-tail
// direct providers keep their static lists in useModelWhitelist — the backend
// has no empirical source for those.
const API_PLATFORMS: Record<string, GroupPlatform> = {
  anthropic: 'anthropic',
  claude: 'anthropic',
  openai: 'openai',
  gemini: 'gemini',
  antigravity: 'antigravity'
}

// Module-level reactive cache keyed by BACKEND platform, shared across all
// selector instances (one fetch serves all; refreshed on page reload).
const cache = reactive<Record<string, string[]>>({})
const inflight = new Set<string>()
const loading = ref(false)
const error = ref<string | null>(null)

export function isApiBackedPlatform(platform: string): boolean {
  return platform in API_PLATFORMS
}

// One representative frontend name per backend platform — used by the selector's
// no-platform ("all models") case to fetch + union every self-healing list.
export const apiBackedPlatforms: readonly string[] = ['anthropic', 'openai', 'gemini', 'antigravity']

// servableModelsFor returns the cached self-healing list for an API-backed
// platform (reactive — Vue computeds re-run when the fetch resolves), `[]` while
// a fetch is still pending, or undefined for a non-API platform (the caller then
// falls back to its static list).
export function servableModelsFor(platform: string): string[] | undefined {
  const backend = API_PLATFORMS[platform]
  if (!backend) return undefined
  return cache[backend] ?? []
}

export function useServableModels() {
  // ensureLoaded fetches the per-platform self-healing candidate list once and
  // caches it. id=0 → platform defaults (the candidate list with no
  // account-specific additions), which is exactly what the selector needs.
  async function ensureLoaded(platform: string): Promise<void> {
    const backend = API_PLATFORMS[platform]
    if (!backend || cache[backend] !== undefined || inflight.has(backend)) return
    inflight.add(backend)
    loading.value = true
    error.value = null
    try {
      cache[backend] = await getModelsListCandidates(0, backend)
    } catch (e) {
      // The axios interceptor (api/client.ts) rejects with a flattened plain
      // object { status, code, message, ... }, not an Error — so String(e) would
      // store "[object Object]". Pull the backend message via the shared helper.
      error.value = unknownToErrorMessage(e, 'Failed to load servable models')
      cache[backend] = [] // loaded-empty; the selector's custom input stays the escape hatch
    } finally {
      inflight.delete(backend)
      loading.value = false
    }
  }

  return { ensureLoaded, servableModelsFor, isApiBackedPlatform, loading, error }
}
