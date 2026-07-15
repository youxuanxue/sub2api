<template>
  <div data-tk="quickstart-client-picker" class="space-y-5">
    <div
      data-tk="quickstart-support-legend"
      class="flex flex-wrap gap-x-4 gap-y-1 text-xs text-gray-500 dark:text-gray-400"
    >
      <span
        v-for="tier in supportTierOrder"
        :key="tier"
        class="inline-flex items-center gap-1"
      >
        <Icon
          :name="supportMeta[tier].icon"
          size="xs"
          aria-hidden="true"
          :class="supportMeta[tier].className"
        />
        {{ supportMeta[tier].label }}
      </span>
    </div>

    <p
      v-if="hasUnavailableClients"
      data-tk="quickstart-unavailable-hint"
      class="text-xs text-amber-700 dark:text-amber-300"
    >
      {{ t('quickstart.unavailableHint') }}
    </p>

    <section
      v-for="group in groups"
      :key="group.id"
      :data-tk="`quickstart-client-group-${group.id}`"
      class="space-y-2"
    >
      <h3 class="text-xs font-semibold text-gray-500 dark:text-gray-400">
        {{ group.label }}
      </h3>

      <div class="grid grid-cols-2 gap-2 sm:grid-cols-4 lg:grid-cols-7">
        <div
          v-for="client in group.clients"
          :key="client.id"
          class="group relative min-w-0"
        >
          <button
            type="button"
            :data-tk="`quickstart-client-${client.id}`"
            :data-support-tier="client.supportTier"
            :aria-pressed="selectedId === client.id"
            :data-unavailable="client.disabled || undefined"
            :aria-describedby="client.disabled && client.disabledReason ? reasonId(group.id, client.id) : undefined"
            :title="clientTitle(client)"
            :class="[
              'flex h-12 w-full min-w-0 items-center gap-2 rounded-lg border px-3 text-left text-sm font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary-500 focus-visible:ring-offset-2 dark:focus-visible:ring-offset-dark-900',
              selectedId === client.id
                ? 'border-primary-500 bg-primary-50 text-primary-700 dark:border-primary-500 dark:bg-primary-900/25 dark:text-primary-300'
                : 'border-gray-200 bg-white text-gray-700 hover:border-gray-300 hover:bg-gray-50 dark:border-dark-600 dark:bg-dark-800 dark:text-gray-200 dark:hover:border-dark-500 dark:hover:bg-dark-700',
              client.disabled
                ? 'cursor-not-allowed opacity-55 hover:border-gray-200 hover:bg-white dark:hover:border-dark-600 dark:hover:bg-dark-800'
                : 'cursor-pointer'
            ]"
            @click="selectClient(client)"
          >
            <Icon :name="client.icon" size="sm" aria-hidden="true" class="shrink-0" />
            <span class="min-w-0 flex-1 truncate">{{ client.name }}</span>
            <Icon
              :name="supportMeta[client.supportTier].icon"
              size="xs"
              aria-hidden="true"
              :class="['shrink-0', supportMeta[client.supportTier].className]"
            />
            <span class="sr-only">{{ supportMeta[client.supportTier].label }}</span>
          </button>

          <span
            v-if="client.disabled && client.disabledReason"
            :id="reasonId(group.id, client.id)"
            role="tooltip"
            class="pointer-events-none absolute left-1/2 top-full z-20 mt-1 hidden w-max max-w-64 -translate-x-1/2 rounded-md bg-gray-900 px-2 py-1 text-center text-xs font-normal text-white shadow-lg sm:group-hover:block sm:group-focus-within:block dark:bg-gray-100 dark:text-gray-900"
          >
            {{ client.disabledReason }}
          </span>
        </div>
      </div>
    </section>
  </div>
</template>

<script lang="ts">
import type IconComponent from '@/components/icons/Icon.vue'

export type QuickstartClientIconName = InstanceType<typeof IconComponent>['$props']['name']

export type QuickstartClientSupportTier = 'verified' | 'import' | 'compatible'

export interface QuickstartClientOption {
  id: string
  name: string
  icon: QuickstartClientIconName
  supportTier: QuickstartClientSupportTier
  disabled?: boolean
  disabledReason?: string
}

export interface QuickstartClientGroup {
  id: string
  label: string
  clients: QuickstartClientOption[]
}
</script>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'

const { t } = useI18n()

const props = defineProps<{
  groups: QuickstartClientGroup[]
  selectedId?: string | null
}>()

const emit = defineEmits<{
  select: [id: string]
}>()

const supportMeta = computed<Record<
  QuickstartClientSupportTier,
  { icon: QuickstartClientIconName; label: string; className: string }
>>(() => ({
  verified: { icon: 'checkCircle', label: t('quickstart.supportVerified'), className: 'text-emerald-500' },
  import: { icon: 'download', label: t('quickstart.supportImport'), className: 'text-primary-500' },
  compatible: { icon: 'link', label: t('quickstart.supportCompatible'), className: 'text-gray-400 dark:text-gray-500' },
}))

const supportTierOrder: QuickstartClientSupportTier[] = ['verified', 'import', 'compatible']

const hasUnavailableClients = computed(() =>
  props.groups.some((group) => group.clients.some((client) => client.disabled)),
)

function selectClient(client: QuickstartClientOption): void {
  emit('select', client.id)
}

function clientTitle(client: QuickstartClientOption): string {
  if (client.disabled) return client.disabledReason || t('quickstart.unavailableProtocol')
  return `${client.name} - ${supportMeta.value[client.supportTier].label}`
}

function reasonId(groupId: string, clientId: string): string {
  return `quickstart-client-${groupId}-${clientId}-reason`
}
</script>
