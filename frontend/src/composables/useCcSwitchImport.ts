import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import type { ApiKey } from '@/types'
import {
  CC_SWITCH_USAGE_SCRIPT,
  buildCcSwitchImportDeeplink,
  type CcSwitchApp,
} from '@/utils/ccswitchImport'

export interface CcSwitchImportParams {
  key: ApiKey
  ccsApp: CcSwitchApp
  baseUrl: string
  providerName: string
}

const NOT_INSTALLED_FOCUS_MS = 100

export function executeCcSwitchImport(
  params: CcSwitchImportParams,
  onNotInstalled?: () => void,
): boolean {
  const deeplink = buildCcSwitchImportDeeplink({
    baseUrl: params.baseUrl,
    platform: params.key.group?.platform ?? null,
    ccsApp: params.ccsApp,
    providerName: params.providerName,
    apiKey: params.key.key,
    usageScript: CC_SWITCH_USAGE_SCRIPT,
  })

  try {
    window.open(deeplink, '_self')
    window.setTimeout(() => {
      if (document.hasFocus()) {
        onNotInstalled?.()
      }
    }, NOT_INSTALLED_FOCUS_MS)
    return true
  } catch {
    return false
  }
}

export function useCcSwitchImport() {
  const { t } = useI18n()
  const appStore = useAppStore()

  function importToCcSwitch(params: CcSwitchImportParams): void {
    const notifyNotInstalled = () => appStore.showError(t('keys.ccSwitchNotInstalled'))
    const launched = executeCcSwitchImport(params, notifyNotInstalled)
    if (!launched) {
      notifyNotInstalled()
    }
  }

  return { importToCcSwitch }
}
