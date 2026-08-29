// TokenKey-only supplier-source i18n overlay.
//
// Kept OUT of the upstream admin/index.ts aggregators so those stay
// merge-safe (CLAUDE.md §5). Deep-merged by i18n/index.ts.

import enSupplierSources from '../locales/en/admin/supplierSources'
import zhSupplierSources from '../locales/zh/admin/supplierSources'

export default {
  en: { admin: enSupplierSources },
  zh: { admin: zhSupplierSources },
}
