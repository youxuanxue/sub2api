<template>
  <div class="space-y-1">
    <div
      v-if="!quotaVisible && !balanceVisible"
      class="text-xs text-gray-400"
      :title="t('admin.accounts.cnProviders.noBalanceEndpoint')"
    >-</div>
    <CNProviderQuotaCell :account="account" />
    <CNProviderBalanceCell :account="account" />
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { Account } from '@/types'
import CNProviderQuotaCell from '../CNProviderQuotaCell.vue'
import CNProviderBalanceCell from '../CNProviderBalanceCell.vue'
import { cnQuotaCellVisible, cnBalanceCellVisible } from '../credentialsBuilder'

const props = defineProps<{ account: Account }>()
const { t } = useI18n()
const mode = computed(() => typeof props.account.credentials?.account_mode === 'string'
  ? props.account.credentials.account_mode : '')
const quotaVisible = computed(() => cnQuotaCellVisible(props.account.platform, mode.value))
const balanceVisible = computed(() => cnBalanceCellVisible(props.account.platform, mode.value))
</script>
