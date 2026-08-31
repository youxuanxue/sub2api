import { computed, shallowRef } from 'vue'
import { useI18n } from 'vue-i18n'

export interface SupplierManagedAccountLike {
  extra?: Record<string, unknown> | null
}

export interface SupplierManagedAccountInfo {
  managed: boolean
  sourceId: number | null
  label: string
  href: string
}

const sourceNamesByID = shallowRef(new Map<number, string>())
let sourceDirectoryPromise: Promise<void> | null = null

function supplierSourceMarker(account: SupplierManagedAccountLike | null | undefined): {
  managed: boolean
  sourceId: number | null
} {
  const extra = account?.extra
  if (!extra || typeof extra !== 'object' || !Object.prototype.hasOwnProperty.call(extra, 'supplier_source_id')) {
    return { managed: false, sourceId: null }
  }

  const rawID = extra.supplier_source_id
  if (typeof rawID === 'number' && Number.isSafeInteger(rawID) && rawID > 0) {
    return { managed: true, sourceId: rawID }
  }
  if (typeof rawID === 'string') {
    const normalized = rawID.trim()
    if (/^[1-9]\d*$/.test(normalized)) {
      const parsed = Number(normalized)
      if (Number.isSafeInteger(parsed)) {
        return { managed: true, sourceId: parsed }
      }
    }
  }
  return { managed: true, sourceId: null }
}

async function ensureSourceDirectoryLoaded(): Promise<void> {
  if (sourceDirectoryPromise) return sourceDirectoryPromise

  sourceDirectoryPromise = import('@/api/admin/supplierSources')
    .then(({ default: supplierSourcesAPI }) => supplierSourcesAPI.list())
    .then((sources) => {
      sourceNamesByID.value = new Map(sources.map(source => [
        source.id,
        `${source.supplier_name}/${source.channel_name}`
      ]))
    })
    .finally(() => {
      sourceDirectoryPromise = null
    })

  return sourceDirectoryPromise
}

export function useSupplierManagedAccount() {
  const { t } = useI18n()
  const viewHint = computed(() => t('admin.accounts.supplierManaged.viewHint'))

  const inspect = (account: SupplierManagedAccountLike | null | undefined): SupplierManagedAccountInfo => {
    const marker = supplierSourceMarker(account)
    if (!marker.managed) {
      return { managed: false, sourceId: null, label: '', href: '' }
    }

    const baseLabel = t('admin.accounts.supplierManaged.badge')
    if (marker.sourceId === null) {
      return {
        managed: true,
        sourceId: null,
        label: baseLabel,
        href: '/admin/supplier-sources'
      }
    }

    const sourceName = sourceNamesByID.value.get(marker.sourceId)
    return {
      managed: true,
      sourceId: marker.sourceId,
      label: sourceName ? `${baseLabel} · ${sourceName}` : `${baseLabel} #${marker.sourceId}`,
      href: `/admin/supplier-sources?source_id=${marker.sourceId}`
    }
  }

  return {
    inspect,
    viewHint,
    ensureSourceDirectoryLoaded
  }
}
