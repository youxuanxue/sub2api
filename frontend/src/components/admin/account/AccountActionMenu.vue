<template>
  <Teleport to="body">
    <div v-if="show && (position || anchor)">
      <!-- Backdrop: click anywhere outside to close -->
      <div class="fixed inset-0 z-[9998]" @click="emit('close')"></div>
      <div
        ref="contentRef"
        class="action-menu-content fixed z-[9999] w-52 overflow-hidden rounded-xl bg-white shadow-lg ring-1 ring-black/5 dark:bg-dark-800"
        :style="menuStyle"
        @click.stop
      >
        <div class="py-1">
          <template v-if="account">
            <button @click.stop.prevent="emitAction('test', account)" class="flex w-full items-center gap-2 px-4 py-2 text-sm hover:bg-gray-100 dark:hover:bg-dark-700">
              <Icon name="play" size="sm" class="text-green-500" :stroke-width="2" />
              {{ t('admin.accounts.testConnection') }}
            </button>
            <button @click.stop.prevent="emitAction('stats', account)" class="flex w-full items-center gap-2 px-4 py-2 text-sm hover:bg-gray-100 dark:hover:bg-dark-700">
              <Icon name="chart" size="sm" class="text-indigo-500" />
              {{ t('admin.accounts.viewStats') }}
            </button>
            <button @click.stop.prevent="emitAction('schedule', account)" class="flex w-full items-center gap-2 px-4 py-2 text-sm hover:bg-gray-100 dark:hover:bg-dark-700">
              <Icon name="clock" size="sm" class="text-orange-500" />
              {{ t('admin.scheduledTests.schedule') }}
            </button>
            <!-- 影子账号不持凭据:重授权/刷新 token 对其无效(后端拒绝),故隐藏(外审 G4)。 -->
            <template v-if="(account.type === 'oauth' || account.type === 'setup-token') && !isShadow">
              <button @click.stop.prevent="emitAction('reauth', account)" class="flex w-full items-center gap-2 px-4 py-2 text-sm text-blue-600 hover:bg-gray-100 dark:hover:bg-dark-700">
                <Icon name="link" size="sm" />
                {{ t('admin.accounts.reAuthorize') }}
              </button>
              <button @click.stop.prevent="emitAction('refresh-token', account)" class="flex w-full items-center gap-2 px-4 py-2 text-sm text-purple-600 hover:bg-gray-100 dark:hover:bg-dark-700">
                <Icon name="refresh" size="sm" />
                {{ t('admin.accounts.refreshToken') }}
              </button>
            </template>
            <button v-if="isOpenAIOAuthParent" @click.stop.prevent="emitAction('create-spark-shadow', account)" class="flex w-full items-center gap-2 px-4 py-2 text-sm text-amber-600 hover:bg-gray-100 dark:hover:bg-dark-700">
              <Icon name="sparkles" size="sm" />
              {{ t('admin.accounts.createSparkShadow') }}
            </button>
            <button v-if="supportsPrivacy" @click.stop.prevent="emitAction('set-privacy', account)" class="flex w-full items-center gap-2 px-4 py-2 text-sm text-emerald-600 hover:bg-gray-100 dark:hover:bg-dark-700">
              <Icon name="shield" size="sm" />
              {{ t('admin.accounts.setPrivacy') }}
            </button>
            <button v-if="canSetTier" @click.stop.prevent="emitAction('set-tier', account)" class="flex w-full items-center gap-2 px-4 py-2 text-sm text-amber-600 hover:bg-gray-100 dark:hover:bg-dark-700">
              <Icon name="chart" size="sm" />
              {{ t('admin.accounts.setTierDialog.menuItem') }}
            </button>
            <div class="my-1 border-t border-gray-100 dark:border-dark-700"></div>
            <button @click.stop.prevent="emitAction('recover-state', account)" class="flex w-full items-center gap-2 px-4 py-2 text-sm text-emerald-600 hover:bg-gray-100 dark:hover:bg-dark-700" :title="t('admin.accounts.recoverStateHint')">
              <Icon name="sync" size="sm" />
              {{ t('admin.accounts.recoverState') }}
            </button>
            <button v-if="hasQuotaLimit" @click.stop.prevent="emitAction('reset-quota', account)" class="flex w-full items-center gap-2 px-4 py-2 text-sm text-teal-600 hover:bg-gray-100 dark:hover:bg-dark-700">
              <Icon name="refresh" size="sm" />
              {{ t('admin.accounts.resetQuota') }}
            </button>
          </template>
        </div>
      </div>
    </div>
  </Teleport>
</template>

<script setup lang="ts">
import { computed, watch, onUnmounted, nextTick, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Icon } from '@/components/icons'
import type { Account } from '@/types'
import { PLATFORM_ANTHROPIC, PLATFORM_ANTIGRAVITY, PLATFORM_OPENAI } from '@/constants/gatewayPlatforms'

const props = defineProps<{
  show: boolean
  account: Account | null
  position: { top: number; left: number } | null
  anchor?: HTMLElement | null
}>()
const emit = defineEmits(['close', 'test', 'stats', 'schedule', 'reauth', 'refresh-token', 'recover-state', 'reset-quota', 'set-privacy', 'set-tier', 'create-spark-shadow'])
const { t } = useI18n()

const DEFAULT_MENU_WIDTH = 208
const DEFAULT_MENU_HEIGHT = 240
const VIEWPORT_PADDING = 8
const contentRef = ref<HTMLElement | null>(null)
const menuStyle = ref<Record<string, string>>({ top: '0px', left: '0px' })
let positionRaf = 0

const clamp = (value: number, min: number, max: number) => Math.max(min, Math.min(value, max))
const requestPositionFrame = (callback: FrameRequestCallback) => {
  if (typeof window.requestAnimationFrame === 'function') {
    return window.requestAnimationFrame(callback)
  }
  return window.setTimeout(() => callback(window.performance?.now?.() ?? Date.now()), 0)
}
const cancelPositionFrame = (handle: number) => {
  if (typeof window.cancelAnimationFrame === 'function') {
    window.cancelAnimationFrame(handle)
    return
  }
  window.clearTimeout(handle)
}

const resolveMenuSize = () => ({
  width: contentRef.value?.offsetWidth || DEFAULT_MENU_WIDTH,
  height: contentRef.value?.offsetHeight || DEFAULT_MENU_HEIGHT
})

const updateMenuPosition = () => {
  if (!props.show) return
  const { width, height } = resolveMenuSize()
  const viewportWidth = window.innerWidth
  const viewportHeight = window.innerHeight

  let left = props.position?.left ?? VIEWPORT_PADDING
  let top = props.position?.top ?? VIEWPORT_PADDING

  if (props.anchor?.isConnected) {
    const rect = props.anchor.getBoundingClientRect()
    if (viewportWidth < 768) {
      left = rect.left + rect.width / 2 - width / 2
    } else {
      left = rect.right - width
    }
    top = rect.bottom + 4

    if (top + height > viewportHeight - VIEWPORT_PADDING) {
      top = rect.top - height - 4
    }
  }

  const maxLeft = Math.max(VIEWPORT_PADDING, viewportWidth - width - VIEWPORT_PADDING)
  const maxTop = Math.max(VIEWPORT_PADDING, viewportHeight - height - VIEWPORT_PADDING)
  menuStyle.value = {
    left: `${clamp(left, VIEWPORT_PADDING, maxLeft)}px`,
    top: `${clamp(top, VIEWPORT_PADDING, maxTop)}px`
  }
}

const schedulePositionUpdate = () => {
  if (!props.show) return
  void nextTick(() => {
    if (positionRaf) cancelPositionFrame(positionRaf)
    positionRaf = requestPositionFrame(() => {
      positionRaf = 0
      updateMenuPosition()
    })
  })
}

type MenuActionEvent =
  | 'test'
  | 'stats'
  | 'schedule'
  | 'reauth'
  | 'refresh-token'
  | 'recover-state'
  | 'reset-quota'
  | 'set-privacy'
  | 'set-tier'
  | 'create-spark-shadow'

// Defer menu teardown until after the click finishes. Synchronous close unmounts
// the z-[9998] backdrop while the pointer is still over the row "more" trigger;
// the ghost click reopens the menu above the test modal (z-50), which looks like
// "flash, no dialog" to operators (prod cc-us5 repro).
const emitAction = (event: MenuActionEvent, account: Account) => {
  emit(event, account)
  void nextTick(() => emit('close'))
}
const isAntigravityOAuth = computed(() => props.account?.platform === PLATFORM_ANTIGRAVITY && props.account?.type === 'oauth')
const isOpenAIOAuth = computed(() => props.account?.platform === PLATFORM_OPENAI && props.account?.type === 'oauth')
// 影子账号(链接型,持 parent_account_id)不持凭据、type 不可变,凭据/隐私类操作对其无效。
const isShadow = computed(() => props.account?.parent_account_id != null)
// A "parent" OpenAI OAuth account is one that is NOT itself a shadow (parent_account_id == null)
const isOpenAIOAuthParent = computed(() => isOpenAIOAuth.value && !isShadow.value)
const supportsPrivacy = computed(() => (isAntigravityOAuth.value || isOpenAIOAuth.value) && !isShadow.value)
const isAnthropicOAuthPassthrough = computed(
  () => (props.account?.extra as Record<string, unknown> | undefined)?.anthropic_oauth_passthrough === true
)
// Tier (5h window / session control) only applies to non-passthrough anthropic
// OAUTH and setup-token accounts — mirrors backend AccountTierService.applyTier,
// which rejects api-key / mirror-stub / passthrough accounts.
const canSetTier = computed(
  () =>
    props.account?.platform === PLATFORM_ANTHROPIC &&
    (props.account?.type === 'oauth' || props.account?.type === 'setup-token') &&
    !isAnthropicOAuthPassthrough.value
)
const hasQuotaLimit = computed(() => {
  return (props.account?.type === 'apikey' || props.account?.type === 'bedrock') && (
    (props.account?.quota_limit ?? 0) > 0 ||
    (props.account?.quota_daily_limit ?? 0) > 0 ||
    (props.account?.quota_weekly_limit ?? 0) > 0
  )
})

const handleKeydown = (event: KeyboardEvent) => {
  if (event.key === 'Escape') emit('close')
}

watch(
  () => props.show,
  (visible) => {
    if (visible) {
      window.addEventListener('keydown', handleKeydown)
      window.addEventListener('resize', schedulePositionUpdate)
      schedulePositionUpdate()
    } else {
      window.removeEventListener('keydown', handleKeydown)
      window.removeEventListener('resize', schedulePositionUpdate)
    }
  },
  { immediate: true }
)

watch(
  () => [props.anchor, props.position?.top, props.position?.left, props.account?.id],
  schedulePositionUpdate,
  { flush: 'post' }
)

onUnmounted(() => {
  if (positionRaf) cancelPositionFrame(positionRaf)
  window.removeEventListener('keydown', handleKeydown)
  window.removeEventListener('resize', schedulePositionUpdate)
})
</script>
