/**
 * TokenKey-only: state + side-effects for the per-account "设置 Tier" action.
 *
 * Keeps AccountsView.vue thin (CLAUDE.md §5): the view only opens/closes the
 * modal via `target`/`open`/`close` and calls `apply`. All API wiring, busy
 * state, and toasts live here.
 */
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api'
import { useAppStore } from '@/stores'
import { ACCOUNT_TIER_OPTIONS } from '@/constants/accountTierOptions.tk'
import type { Account } from '@/types'

export function useTkAccountTier(onApplied: (account: Account) => void) {
  const { t } = useI18n()
  const appStore = useAppStore()

  const show = ref(false)
  const target = ref<Account | null>(null)
  const selectedTier = ref<string>('')
  const submitting = ref(false)

  const tierOptions = ACCOUNT_TIER_OPTIONS

  function open(account: Account) {
    target.value = account
    // Prefill with the account's current tier if it carries one.
    const current = (account.extra as Record<string, unknown> | undefined)?.stability_tier
    selectedTier.value = typeof current === 'string' ? current : ''
    show.value = true
  }

  function close() {
    show.value = false
    target.value = null
    selectedTier.value = ''
    submitting.value = false
  }

  async function apply() {
    if (!target.value || !selectedTier.value || submitting.value) return
    submitting.value = true
    try {
      const updated = await adminAPI.accounts.applyAccountTier(target.value.id, selectedTier.value)
      onApplied(updated)
      appStore.showSuccess(t('admin.accounts.setTierDialog.applySuccess'))
      close()
    } catch (error: any) {
      console.error('Failed to apply account tier:', error)
      appStore.showError(error?.response?.data?.message || error?.message || t('admin.accounts.setTierDialog.applyFailed'))
      submitting.value = false
    }
  }

  return { show, target, selectedTier, submitting, tierOptions, open, close, apply }
}
