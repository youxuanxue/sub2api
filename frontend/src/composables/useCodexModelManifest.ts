import { computed, onBeforeUnmount, ref, watch, type Ref } from 'vue'
import { saveAs } from 'file-saver'
import { fetchCodexModelsManifest } from '@/api/codex'
import { parseCodexCatalogModels } from '@/utils/codexCatalogConfig'

export function useCodexModelManifest(baseUrl: Ref<string>, apiKey: Ref<string>) {
  const state = ref<'idle' | 'loading' | 'ready' | 'error'>('idle')
  const content = ref('')
  const models = computed(() => state.value === 'ready' ? parseCodexCatalogModels(content.value) : null)
  let controller: AbortController | null = null
  let generation = 0

  function reset() {
    generation += 1
    controller?.abort()
    controller = null
    content.value = ''
    state.value = 'idle'
  }

  async function load(): Promise<boolean> {
    if (!apiKey.value) return false
    controller?.abort()
    const current = ++generation
    const request = new AbortController()
    controller = request
    state.value = 'loading'
    try {
      const result = await fetchCodexModelsManifest(baseUrl.value, apiKey.value, request.signal)
      if (current !== generation) return false
      content.value = result.content
      state.value = 'ready'
      return true
    } catch {
      if (current === generation && !request.signal.aborted) state.value = 'error'
      return false
    } finally {
      if (current === generation) controller = null
    }
  }

  function download() {
    if (state.value !== 'ready') return
    saveAs(new Blob([content.value], { type: 'application/json;charset=utf-8' }), 'codex-models.json')
  }

  watch([baseUrl, apiKey], reset, { flush: 'sync' })
  onBeforeUnmount(reset)
  return { state, content, models, load, download }
}
