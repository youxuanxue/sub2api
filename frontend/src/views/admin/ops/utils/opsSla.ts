export type OpsSlaOverview = {
  sla?: number
  request_count_total?: number
}

/** Hide SLA when the window has no requests. Backend never sends request_count_sla. */
export function resolveOpsSlaPercent(overview: OpsSlaOverview | null | undefined): number | null {
  if (overview == null || typeof overview.sla !== 'number' || !Number.isFinite(overview.sla)) {
    return null
  }
  if ((overview.request_count_total ?? 0) <= 0) return null
  return overview.sla * 100
}

export function shouldFillHeaderOverview(snapshotDone: boolean, overviewApplied: boolean): boolean {
  return !snapshotDone && !overviewApplied
}
