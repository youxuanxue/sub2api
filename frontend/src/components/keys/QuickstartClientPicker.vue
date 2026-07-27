<template>
  <div data-tk="quickstart-client-picker" class="space-y-3">
    <div class="flex flex-wrap items-baseline gap-x-4 gap-y-1">
      <h2 class="text-sm font-semibold text-gray-900 dark:text-white">
        {{ heading }}
      </h2>

      <div
        data-tk="quickstart-support-legend"
        class="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-0.5 text-[11px] leading-snug text-gray-500 dark:text-gray-400"
      >
        <span
          v-for="(tier, index) in supportTierOrder"
          :key="tier"
          class="inline-flex items-center gap-1"
        >
          <span v-if="index > 0" aria-hidden="true" class="text-gray-300 dark:text-dark-500">·</span>
          <Icon
            :name="supportMeta[tier].icon"
            size="xs"
            aria-hidden="true"
            :class="supportMeta[tier].iconClass"
          />
          <span>{{ supportMeta[tier].label }}</span>
          <span class="text-gray-400 dark:text-gray-500">— {{ supportMeta[tier].detail }}</span>
        </span>
      </div>
    </div>

    <p
      v-if="hasUnavailableClients"
      data-tk="quickstart-unavailable-hint"
      class="text-xs text-amber-700 dark:text-amber-300"
    >
      {{ t('quickstart.unavailableHint') }}
    </p>

    <div class="space-y-2.5">
      <section
        v-for="group in groups"
        :key="group.id"
        :data-tk="`quickstart-client-group-${group.id}`"
        class="space-y-1.5 sm:grid sm:grid-cols-[6.5rem_minmax(0,1fr)] sm:items-start sm:gap-3 sm:space-y-0"
      >
        <h3 class="pt-0.5 text-xs font-semibold text-gray-500 dark:text-gray-400 sm:pt-3">
          {{ group.label }}
        </h3>

        <div class="grid grid-cols-[repeat(auto-fit,minmax(8.75rem,1fr))] gap-1.5">
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
                'flex h-11 w-full min-w-0 items-center gap-2 rounded-md border px-2.5 text-left text-sm font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary-500 focus-visible:ring-offset-2 sm:h-10 dark:focus-visible:ring-offset-dark-900',
                selectedId === client.id
                  ? 'border-primary-500 bg-primary-50 text-primary-700 dark:border-primary-500 dark:bg-primary-900/25 dark:text-primary-300'
                  : 'border-gray-200 bg-white text-gray-700 hover:border-gray-300 hover:bg-gray-50 dark:border-dark-600 dark:bg-dark-800 dark:text-gray-200 dark:hover:border-dark-500 dark:hover:bg-dark-700',
                client.disabled
                  ? 'cursor-pointer opacity-55 hover:border-amber-300 hover:bg-amber-50 dark:hover:border-amber-700 dark:hover:bg-amber-900/15'
                  : 'cursor-pointer'
              ]"
              @click="selectClient(client)"
            >
              <Icon :name="client.icon" size="sm" aria-hidden="true" class="shrink-0" />
              <span class="min-w-0 flex-1 truncate">{{ client.name }}</span>
              <span
                data-tk="quickstart-tier-badge"
                class="inline-flex shrink-0 items-center justify-center rounded-md p-1"
                :class="supportMeta[client.supportTier].badgeClass"
                :aria-label="`${supportMeta[client.supportTier].label}: ${supportMeta[client.supportTier].detail}`"
                :title="`${supportMeta[client.supportTier].label}: ${supportMeta[client.supportTier].detail}`"
              >
                <Icon
                  :name="supportMeta[client.supportTier].icon"
                  size="xs"
                  aria-hidden="true"
                />
              </span>
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

        <p
          v-if="selectedUnavailableInGroup(group)"
          data-tk="quickstart-client-unavailable"
          class="rounded-md border border-amber-200 bg-amber-50 px-3 py-2 text-xs text-amber-800 dark:border-amber-800 dark:bg-amber-900/20 dark:text-amber-200 sm:col-start-2"
        >
          {{ selectedUnavailableInGroup(group)?.disabledReason }}
        </p>
      </section>
    </div>
  </div>
</template>

<script lang="ts">
import type IconComponent from '@/components/icons/Icon.vue'
import type { TkClientSupportTier } from '@/constants/clientIntegrations.tk'

export type QuickstartClientIconName = InstanceType<typeof IconComponent>['$props']['name']

export type QuickstartClientSupportTier = TkClientSupportTier

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
import { TK_CLIENT_SUPPORT_META } from '@/constants/clientIntegrations.tk'

const { t } = useI18n()

const props = defineProps<{
  heading: string
  groups: QuickstartClientGroup[]
  selectedId?: string | null
}>()

const emit = defineEmits<{
  select: [id: string]
}>()

type PickerSupportMeta = Record<QuickstartClientSupportTier, {
  icon: QuickstartClientIconName
  label: string
  detail: string
  iconClass: string
  badgeClass: string
}>

const supportTierOrder: QuickstartClientSupportTier[] = ['verified', 'import', 'compatible']

const supportMeta = computed<PickerSupportMeta>(() =>
  supportTierOrder.reduce<PickerSupportMeta>((result, tier) => {
    result[tier] = {
      icon: TK_CLIENT_SUPPORT_META[tier].icon,
      label: t(TK_CLIENT_SUPPORT_META[tier].labelKey),
      detail: t(TK_CLIENT_SUPPORT_META[tier].detailKey),
      iconClass: TK_CLIENT_SUPPORT_META[tier].legendClass,
      badgeClass: TK_CLIENT_SUPPORT_META[tier].badgeClass,
    }
    return result
  }, {} as PickerSupportMeta),
)

const hasUnavailableClients = computed(() =>
  props.groups.some((group) => group.clients.some((client) => client.disabled)),
)

function selectClient(client: QuickstartClientOption): void {
  emit('select', client.id)
}

function selectedUnavailableInGroup(group: QuickstartClientGroup): QuickstartClientOption | undefined {
  return group.clients.find((client) => client.id === props.selectedId && client.disabled)
}

function clientTitle(client: QuickstartClientOption): string {
  if (client.disabled) return client.disabledReason || t('quickstart.unavailableProtocol')
  const meta = supportMeta.value[client.supportTier]
  return `${client.name} - ${meta.label}: ${meta.detail}`
}

function reasonId(groupId: string, clientId: string): string {
  return `quickstart-client-${groupId}-${clientId}-reason`
}
</script>
