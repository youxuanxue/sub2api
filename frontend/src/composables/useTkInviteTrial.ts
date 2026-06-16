/**
 * TokenKey Invite-to-Trial composable — owns the form/preset/group state, the
 * batch submit, and the credential-card results for the InviteTrialModal.
 *
 * TK-only flow (CLAUDE.md §5): view + modal stay thin; this composable owns the
 * API calls and derived state.
 */
import { computed, reactive, ref } from 'vue'
import { adminAPI } from '@/api/admin'
import type { AdminGroup } from '@/types'
import type { TrialCredential, TrialPreset } from '@/api/admin/inviteTrial'

export interface InviteTrialForm {
  presetName: string // '' = custom (inline plan)
  groupId: number | null
  validityDays: number
  balance: number
  concurrency: number
  rpmLimit: number
  rate: number | null
  recipients: string // textarea, one email per line
  autoCount: number
  issueKey: boolean
}

function blankForm(): InviteTrialForm {
  return {
    presetName: '',
    groupId: null,
    validityDays: 30,
    balance: 5,
    concurrency: 1,
    rpmLimit: 0,
    rate: null,
    recipients: '',
    autoCount: 0,
    issueKey: true
  }
}

export function useTkInviteTrial() {
  const form = reactive<InviteTrialForm>(blankForm())
  const presets = ref<TrialPreset[]>([])
  const groups = ref<AdminGroup[]>([])
  const results = ref<TrialCredential[]>([])
  const loading = ref(false)
  const submitting = ref(false)

  // Only subscription-type groups are valid trial targets (expiry = subscription).
  const subscriptionGroups = computed(() =>
    groups.value.filter((g) => g.subscription_type === 'subscription')
  )

  const okCount = computed(() => results.value.filter((r) => !r.error).length)
  const failedCount = computed(() => results.value.filter((r) => r.error).length)

  async function load() {
    loading.value = true
    try {
      const [g, p] = await Promise.all([adminAPI.groups.getAll(), adminAPI.inviteTrial.getPresets()])
      groups.value = g
      presets.value = p
    } finally {
      loading.value = false
    }
  }

  function reset() {
    Object.assign(form, blankForm())
    results.value = []
  }

  // Prefill the inline plan from an existing user's effective config (= "复制账号").
  function seedFromUser(seed: { groupId?: number | null; balance?: number; concurrency?: number; rpmLimit?: number; rate?: number | null }) {
    Object.assign(form, blankForm())
    results.value = []
    form.presetName = ''
    if (seed.groupId != null) form.groupId = seed.groupId
    if (seed.balance != null) form.balance = seed.balance
    if (seed.concurrency != null) form.concurrency = seed.concurrency
    if (seed.rpmLimit != null) form.rpmLimit = seed.rpmLimit
    if (seed.rate != null) form.rate = seed.rate
  }

  function applyPreset(name: string) {
    form.presetName = name
    const p = presets.value.find((x) => x.name === name)
    if (p) {
      form.groupId = p.group_id
      form.validityDays = p.validity_days
      form.balance = p.balance
      form.concurrency = p.concurrency
      form.rpmLimit = p.rpm_limit
      form.rate = p.rate ?? null
    }
  }

  function parsedRecipients(): { email?: string }[] {
    return form.recipients
      .split('\n')
      .map((l) => l.trim())
      .filter((l) => l.length > 0)
      .map((email) => ({ email }))
  }

  /** Returns a validation key for the modal to translate, or null if valid. */
  function validate(): string | null {
    if (!form.presetName && !form.groupId) return 'groupRequired'
    if (parsedRecipients().length === 0 && form.autoCount <= 0) return 'nothingToCreate'
    return null
  }

  async function submit(): Promise<boolean> {
    submitting.value = true
    try {
      const recipients = parsedRecipients()
      results.value = await adminAPI.inviteTrial.inviteTrial({
        preset_name: form.presetName || undefined,
        plan: form.presetName
          ? undefined
          : {
              group_id: form.groupId as number,
              validity_days: form.validityDays,
              balance: form.balance,
              concurrency: form.concurrency,
              rpm_limit: form.rpmLimit,
              rate: form.rate
            },
        recipients: recipients.length ? recipients : undefined,
        auto_count: form.autoCount > 0 ? form.autoCount : undefined,
        issue_key: form.issueKey
      })
      return true
    } finally {
      submitting.value = false
    }
  }

  async function savePreset(name: string): Promise<void> {
    const next: TrialPreset = {
      name,
      group_id: form.groupId as number,
      validity_days: form.validityDays,
      balance: form.balance,
      concurrency: form.concurrency,
      rpm_limit: form.rpmLimit,
      rate: form.rate
    }
    const merged = [...presets.value.filter((p) => p.name !== name), next]
    presets.value = await adminAPI.inviteTrial.setPresets(merged)
  }

  return {
    form,
    presets,
    groups,
    subscriptionGroups,
    results,
    loading,
    submitting,
    okCount,
    failedCount,
    load,
    reset,
    seedFromUser,
    applyPreset,
    validate,
    submit,
    savePreset
  }
}
