/**
 * Common component types
 */

export interface Column {
  key: string
  label: string
  sortable?: boolean
  class?: string
  /**
   * Stable column width for fluid tables. When any visible column sets this,
   * the table uses fixed layout so virtualized rows cannot change column
   * widths (and therefore row heights) as the window scrolls.
   */
  width?: string
  formatter?: (value: any, row: any) => string
}
