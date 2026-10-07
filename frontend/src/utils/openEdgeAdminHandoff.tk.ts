import { adminAPI } from '@/api/admin'

/**
 * Open an edge's own /admin/accounts already logged in via the prod→edge
 * admin-session handoff. The blank tab is opened synchronously so the browser
 * does not treat the post-await navigation as a popup.
 */
export async function openEdgeAdminHandoff(edgeId: string): Promise<void> {
  const id = edgeId.trim()
  if (!id) throw new Error('edge id required')
  const tab = window.open('', '_blank')
  try {
    const res = await adminAPI.edgeAccounts.adminSession(id)
    if (tab) tab.location.href = res.handoff_url
    else window.location.href = res.handoff_url
  } catch (err) {
    if (tab) tab.close()
    throw err
  }
}
