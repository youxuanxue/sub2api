import { onBeforeUnmount, ref } from 'vue'
import { edgeAccountsAPI } from '@/api/admin/edgeAccounts'
import { HANDOFF_MESSAGE, handoffProof, handoffTargetURL } from '@/utils/edgeHandoff.tk'

/** One lifecycle for both existing admin entry points. No token storage on prod. */
export function useEdgeAdminHandoff() {
  const managingEdge = ref<string | null>(null)
  const failedEdge = ref<string | null>(null)
  const loginURL = ref('')
  let generation = 0
  let cleanup: (() => void) | undefined
  let disposed = false

  async function openEdgeManage(edgeId: string) {
    if (!edgeId || managingEdge.value || disposed) return
    const run = ++generation
    managingEdge.value = edgeId
    failedEdge.value = null
    loginURL.value = ''
    const tab = window.open('about:blank', '_blank')
    let origin = ''
    let attempt = ''
    let minting = false
    const current = () => !disposed && run === generation
    const finish = (failed: boolean) => {
      if (!current()) return
      cleanup?.()
      managingEdge.value = null
      if (failed) {
        failedEdge.value = edgeId
        tab?.close()
      }
    }
    const receive = async (event: MessageEvent) => {
      if (!current() || !tab || !origin || event.source !== tab || event.origin !== origin || event.data?.type !== HANDOFF_MESSAGE) return
      const data = event.data
      if (data.phase === 'failed') { finish(true); return }
      if (data.phase === 'complete' && attempt && data.attempt === attempt) { finish(false); return }
      if (data.phase !== 'ready' || minting || !handoffProof(data.challenge) || !handoffProof(data.attempt)) return
      minting = true
      attempt = data.attempt
      try {
        const result = await edgeAccountsAPI.adminSession(edgeId, { challenge: data.challenge, attempt })
        if (!current() || tab.closed) return
        if (!handoffProof(result.code) || result.attempt !== attempt || result.edge_id !== edgeId) throw new Error('Invalid handoff')
        tab.postMessage({ type: HANDOFF_MESSAGE, phase: 'code', code: result.code, attempt }, origin)
      } catch { finish(true) }
    }
    cleanup = () => {
      ++generation
      window.removeEventListener('message', receive)
      clearTimeout(timer)
      clearInterval(poll)
      cleanup = undefined
    }
    window.addEventListener('message', receive)
    const timer = setTimeout(() => finish(true), 60000)
    const poll = setInterval(() => { if (tab?.closed) finish(true) }, 400)
    try {
      const target = await edgeAccountsAPI.handoffTarget(edgeId)
      if (!current()) return
      const url = handoffTargetURL(target.handoff_url)
      if (target.edge_id !== edgeId) throw new Error('Invalid edge')
      origin = url.origin
      loginURL.value = `${origin}/login`
      if (!tab || tab.closed || !target.enabled) { finish(true); return }
      tab.location.href = url.href
    } catch { finish(true) }
  }
  onBeforeUnmount(() => { disposed = true; cleanup?.() })
  function retry() { if (failedEdge.value) void openEdgeManage(failedEdge.value) }
  return { managingEdge, failedEdge, loginURL, openEdgeManage, retry }
}
