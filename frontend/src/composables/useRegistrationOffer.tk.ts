import { computed } from 'vue'
import { createSharedComposable, useEventListener, useIntervalFn } from '@vueuse/core'
import { useAppStore } from '@/stores/app'
import type { RegistrationOffer } from '@/types'

/** One lifecycle for every first-party registration promise. */
export const useRegistrationOffer = createSharedComposable(() => {
  const app = useAppStore()
  const offer = computed<RegistrationOffer>(() => {
    const value = app.cachedPublicSettings?.registration_offer
    return value && ['open', 'invitation_required', 'closed', 'unavailable'].includes(value.state)
      ? value : { state: 'unavailable' }
  })
  const canRegister = computed(() => ['open', 'invitation_required'].includes(offer.value.state))
  const bonus = computed(() => offer.value.state === 'open' ? offer.value.signup_bonus_usd : undefined)
  const refresh = () => app.fetchPublicSettings(true)
  useEventListener(document, 'visibilitychange', () => {
    if (document.visibilityState === 'visible') void refresh()
  })
  useIntervalFn(() => {
    if (document.visibilityState === 'visible') void refresh()
  }, 60_000)
  void refresh()
  return { offer, canRegister, bonus, refresh }
})
