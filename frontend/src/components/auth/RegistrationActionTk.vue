<template>
  <div class="flex flex-wrap items-center gap-3" data-tk="registration-action">
    <router-link :to="destination" class="btn btn-primary" data-tk="registration-primary">
      {{ t(primaryLabel) }}
    </router-link>
    <router-link v-if="!auth.isAuthenticated && canRegister" :to="loginDestination" class="text-sm text-primary-600 hover:underline dark:text-primary-400">
      {{ t('onboarding.existingAccount') }}
    </router-link>
    <span v-if="!auth.isAuthenticated" class="text-sm text-gray-500 dark:text-gray-400" role="status">
      {{ message }}
    </span>
    <button v-if="!auth.isAuthenticated && offer.state === 'unavailable'" type="button" class="text-sm text-primary-600 hover:underline" @click="refresh()">
      {{ t('common.retry') }}
    </button>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import { useRegistrationOffer } from '@/composables/useRegistrationOffer.tk'
import { safeInternalRedirect } from '@/utils/quickstartJourney.tk'

const props = withDefaults(defineProps<{ returnTo?: string; forTest?: boolean }>(), { returnTo: '/quickstart', forTest: false })
const { t, locale } = useI18n()
const auth = useAuthStore()
const { offer, canRegister, bonus, refresh } = useRegistrationOffer()
const redirect = computed(() => safeInternalRedirect(props.returnTo, '/quickstart'))
const loginDestination = computed(() => ({ path: '/login', query: { redirect: redirect.value } }))
const destination = computed(() => auth.isAuthenticated ? redirect.value : canRegister.value
  ? { path: '/register', query: { redirect: redirect.value } } : loginDestination.value)
const primaryLabel = computed(() => auth.isAuthenticated ? 'onboarding.openQuickstart'
  : offer.value.state === 'invitation_required' ? 'onboarding.invitationRegister'
  : canRegister.value ? (props.forTest ? 'onboarding.registerAndTest' : 'onboarding.register')
  : (props.forTest ? 'onboarding.loginAndTest' : 'onboarding.login'))
const message = computed(() => {
  if (auth.isAuthenticated) return ''
  if (offer.value.state === 'closed') return t('onboarding.closed')
  if (offer.value.state === 'unavailable') return t('onboarding.unavailable')
  if (offer.value.state === 'invitation_required') return t('onboarding.invitationRequired')
  const amount = Number(bonus.value)
  if (Number.isFinite(amount) && amount > 0) return t('onboarding.bonus', {
    amount: new Intl.NumberFormat(locale.value, { style: 'currency', currency: 'USD' }).format(amount),
  })
  return props.forTest ? t('onboarding.signInToTest') : ''
})
</script>
