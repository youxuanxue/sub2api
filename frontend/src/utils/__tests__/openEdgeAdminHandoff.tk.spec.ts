import { afterEach, describe, expect, it, vi } from 'vitest'

const { adminSession } = vi.hoisted(() => ({
  adminSession: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    edgeAccounts: { adminSession }
  }
}))

import { openEdgeAdminHandoff } from '../openEdgeAdminHandoff.tk'

describe('openEdgeAdminHandoff', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    adminSession.mockReset()
  })

  it('mints then navigates the pre-opened tab to the handoff URL', async () => {
    const tab = { location: { href: '' }, close: vi.fn() }
    vi.stubGlobal('open', vi.fn(() => tab))
    adminSession.mockResolvedValue({
      edge_id: 'uk1',
      handoff_url: 'https://api-uk1.tokenkey.dev/admin/edge-handoff#tk_session=x',
      expires_in: 3600
    })

    await openEdgeAdminHandoff('uk1')

    expect(window.open).toHaveBeenCalledWith('', '_blank')
    expect(adminSession).toHaveBeenCalledWith('uk1')
    expect(tab.location.href).toBe('https://api-uk1.tokenkey.dev/admin/edge-handoff#tk_session=x')
    expect(tab.close).not.toHaveBeenCalled()
  })

  it('closes the blank tab when mint fails so the operator is not left on about:blank', async () => {
    const tab = { location: { href: '' }, close: vi.fn() }
    vi.stubGlobal('open', vi.fn(() => tab))
    adminSession.mockRejectedValue(new Error('edge not found'))

    await expect(openEdgeAdminHandoff('uk1')).rejects.toThrow('edge not found')
    expect(tab.close).toHaveBeenCalled()
  })
})
