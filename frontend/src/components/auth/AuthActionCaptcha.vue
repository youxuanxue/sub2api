<template>
  <CaptchaChallenge
    ref="challengeRef"
    :turnstile-enabled="false"
    turnstile-site-key=""
    :tencent-enabled="tencentEnabled"
    :tencent-app-id="tencentAppId"
    :aliyun-enabled="aliyunEnabled"
    :aliyun-scene-id="aliyunSceneId"
    :aliyun-prefix="aliyunPrefix"
    :aliyun-region="aliyunRegion"
    @error="emit('error')"
  />
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import CaptchaChallenge from '@/components/CaptchaChallenge.vue'
import type { ActionCaptchaRequestProof, PublicSettings } from '@/types'

const props = defineProps<{
  settings: PublicSettings | null
}>()

const emit = defineEmits<{
  error: []
}>()

const challengeRef = ref<InstanceType<typeof CaptchaChallenge> | null>(null)
const tencentEnabled = computed(
  () => props.settings?.tencent_captcha_enabled === true && Boolean(tencentAppId.value)
)
const tencentAppId = computed(() => props.settings?.tencent_captcha_app_id || '')
const aliyunEnabled = computed(
  () =>
    props.settings?.aliyun_captcha_enabled === true &&
    Boolean(aliyunSceneId.value) &&
    Boolean(aliyunPrefix.value)
)
const aliyunSceneId = computed(() => props.settings?.aliyun_captcha_scene_id || '')
const aliyunPrefix = computed(() => props.settings?.aliyun_captcha_prefix || '')
const aliyunRegion = computed(() => props.settings?.aliyun_captcha_region || 'cn')
const enabled = computed(() => tencentEnabled.value || aliyunEnabled.value)

async function acquireProof(): Promise<ActionCaptchaRequestProof | null | undefined> {
  if (!enabled.value) return undefined

  const result = await challengeRef.value?.verifyAction()
  if (!result) return null

  if (tencentEnabled.value) {
    return {
      tencent_captcha_ticket: result.token,
      tencent_captcha_randstr: result.randstr
    }
  }
  return { turnstile_token: result.token }
}

function reset(): void {
  challengeRef.value?.reset()
}

defineExpose({ acquireProof, reset })
</script>
