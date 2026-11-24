export type OpsSlaOverview = {
  sla?: number
  request_count_total?: number
  official_sla?: { incomplete?: boolean; requests: number; faults: number; rate: number | null }
}

/** Reporting-only scope takes precedence; no eligible traffic is not 0% or 100%. */
export function resolveOpsSlaPercent(overview: OpsSlaOverview | null | undefined): number | null {
  const scope = overview?.official_sla
  if (scope) return scope.requests > 0 && scope.rate != null && Number.isFinite(scope.rate) ? scope.rate * 100 : null
  if (overview == null || typeof overview.sla !== 'number' || !Number.isFinite(overview.sla)) {
    return null
  }
  if ((overview.request_count_total ?? 0) <= 0) return null
  return overview.sla * 100
}

export function shouldFillHeaderOverview(snapshotDone: boolean, overviewApplied: boolean): boolean {
  return !snapshotDone && !overviewApplied
}
