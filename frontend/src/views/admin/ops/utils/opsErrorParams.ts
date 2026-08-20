import { resolveOpsCustomTimeRange } from './opsTimeRange'

export function buildOpsErrorTimeParams(
  timeRange: string,
  customStartTime?: string | null,
  customEndTime?: string | null
): Record<string, string> {
  if (timeRange === 'custom' && customStartTime && customEndTime) {
    const resolved = resolveOpsCustomTimeRange(customStartTime, customEndTime)
    if (resolved.ok) {
      return { start_time: resolved.startISO, end_time: resolved.endISO }
    }
  }

  return { time_range: timeRange === 'custom' ? '1h' : timeRange }
}
