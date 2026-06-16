import { describe, expect, it, vi } from 'vitest'

// adminAPI is imported by the composable but only called in load()/submit(); stub
// it so the module imports cleanly under vitest.
vi.mock('@/api/admin', () => ({
  adminAPI: {
    groups: { getAll: vi.fn() },
    inviteTrial: { getPresets: vi.fn(), setPresets: vi.fn(), inviteTrial: vi.fn() }
  }
}))

import { useTkInviteTrial } from '../useTkInviteTrial'

describe('useTkInviteTrial.validate', () => {
  it('requires a group when no preset is chosen', () => {
    const { form, validate } = useTkInviteTrial()
    form.presetName = ''
    form.groupId = null
    form.autoCount = 1
    expect(validate()).toBe('groupRequired')
  })

  it('requires at least one recipient or auto-generate count', () => {
    const { form, validate } = useTkInviteTrial()
    form.groupId = 5
    form.recipients = '   \n  '
    form.autoCount = 0
    expect(validate()).toBe('nothingToCreate')
  })

  it('passes with a group and auto-generate count', () => {
    const { form, validate } = useTkInviteTrial()
    form.groupId = 5
    form.autoCount = 3
    expect(validate()).toBeNull()
  })

  it('passes with a preset and pasted recipients', () => {
    const { form, validate } = useTkInviteTrial()
    form.presetName = 'p1'
    form.recipients = 'a@b.com\nc@d.com'
    expect(validate()).toBeNull()
  })
})

describe('useTkInviteTrial.seedFromUser', () => {
  it('prefills the inline plan and clears any preset', () => {
    const { form, seedFromUser } = useTkInviteTrial()
    form.presetName = 'old'
    seedFromUser({ groupId: 9, balance: 12, concurrency: 4, rpmLimit: 60, rate: 1.5 })
    expect(form.presetName).toBe('')
    expect(form.groupId).toBe(9)
    expect(form.balance).toBe(12)
    expect(form.concurrency).toBe(4)
    expect(form.rpmLimit).toBe(60)
    expect(form.rate).toBe(1.5)
  })
})
