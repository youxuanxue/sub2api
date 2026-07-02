import { reactive, ref } from 'vue'
import { getModelMappingPresets } from '@/api/admin/accounts'
import { unknownToErrorMessage } from '@/utils/authError'

// TK: admin model-whitelist / model_mapping preset SSOT — one backend endpoint
// for native platforms, grok, kiro, and newapi ch41 (Vertex SA). Long-tail direct
// providers keep static lists in useModelWhitelist.
const API_PLATFORMS: Record<string, string> = {
  anthropic: 'anthropic',
  claude: 'anthropic',
  openai: 'openai',
  gemini: 'gemini',
  antigravity: 'antigravity',
  grok: 'grok',
  xai: 'grok',
  kiro: 'kiro',
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
export const apiBackedPlatforms: readonly string[] = [
  'anthropic',
  'openai',
  'gemini',
  'antigravity',
  'grok',
  'kiro',
]

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
  // ensureLoaded fetches the per-platform preset list once and caches it.
  async function ensureLoaded(platform: string): Promise<void> {
    const backend = API_PLATFORMS[platform]
    if (!backend || cache[backend] !== undefined || inflight.has(backend)) return
    inflight.add(backend)
    loading.value = true
    error.value = null
    try {
      cache[backend] = await getModelMappingPresets(backend)
    } catch (e) {
      error.value = unknownToErrorMessage(e, 'Failed to load servable models')
      cache[backend] = []
    } finally {
      inflight.delete(backend)
      loading.value = false
    }
  }

  return { ensureLoaded, servableModelsFor, isApiBackedPlatform, loading, error }
}
