import type { AccountPlatform } from '@/types'

/** Ordered account/group platforms, including the independent fifth platform `newapi`. */
export const GATEWAY_PLATFORMS = ['anthropic', 'openai', 'gemini', 'antigravity', 'newapi'] as const satisfies readonly AccountPlatform[]

/**
 * Platforms that participate in the OpenAI-compatible HTTP request shape
 * (i.e. clients speaking the OpenAI protocol: `/v1/chat/completions`,
 * `/v1/responses`, `/v1/messages` 调度 etc.).
 *
 * Mirrors `service.OpenAICompatPlatforms()` in the Go backend
 * (`backend/internal/service/account_tk_compat_pool.go`). When adding a sixth
 * compat platform, BOTH places must be updated in lockstep — `scripts/preflight.sh`
 * "newapi compat-pool drift" catches the backend half; the frontend half is
 * covered by the `useModelWhitelist` and `usePlatformOptions` test suites.
 */
export const OPENAI_COMPAT_PLATFORMS: readonly AccountPlatform[] = ['openai', 'newapi'] as const

/** Predicate sibling of {@link OPENAI_COMPAT_PLATFORMS} — use whenever a UI branch is gated on "speaks OpenAI HTTP shape". */
export function isOpenAICompatPlatform(platform: string | null | undefined): boolean {
  if (!platform) return false
  return (OPENAI_COMPAT_PLATFORMS as readonly string[]).includes(platform)
}

/**
 * Platforms that have a per-group `messages_dispatch_model_config`
 * (Claude→upstream model mapping form). Wider than {@link OPENAI_COMPAT_PLATFORMS}
 * because gemini-platform groups also use the SAME JSON column to map
 * `claude-*` family/exact requests to gemini model IDs (e.g. gemini-2.5-pro).
 *
 * Mirrors backend predicate `tkGroupKeepsDispatchConfig` in
 * `backend/internal/service/openai_messages_dispatch_tk_newapi.go`. Both lists
 * must move in lockstep; backend sanitizer would otherwise wipe the column on
 * save and the frontend form's value would silently disappear.
 *
 * Differs from {@link isOpenAICompatPlatform} which gates "OpenAI HTTP shape"
 * UI branches (e.g. /v1/chat/completions allowance). Those two questions
 * intentionally do not coincide for gemini.
 */
export const GROUP_DISPATCH_CONFIG_PLATFORMS: readonly AccountPlatform[] = ['openai', 'newapi', 'gemini'] as const

export function hasMessagesDispatchConfig(platform: string | null | undefined): boolean {
  if (!platform) return false
  return (GROUP_DISPATCH_CONFIG_PLATFORMS as readonly string[]).includes(platform)
}

/** Tailwind active-state classes for the create-account platform segmented control (order follows {@link GATEWAY_PLATFORMS}). */
export const CREATE_ACCOUNT_PLATFORM_SEGMENT_ACTIVE: Record<AccountPlatform, string> = {
  anthropic:
    'bg-white text-orange-600 shadow-sm dark:bg-dark-600 dark:text-orange-400',
  openai: 'bg-white text-green-600 shadow-sm dark:bg-dark-600 dark:text-green-400',
  gemini: 'bg-white text-blue-600 shadow-sm dark:bg-dark-600 dark:text-blue-400',
  antigravity:
    'bg-white text-purple-600 shadow-sm dark:bg-dark-600 dark:text-purple-400',
  newapi: 'bg-white text-cyan-600 shadow-sm dark:bg-dark-600 dark:text-cyan-400',
}

export const CREATE_ACCOUNT_PLATFORM_SEGMENT_BASE =
  'flex flex-1 items-center justify-center gap-2 rounded-md px-4 py-2.5 text-sm font-medium transition-all'

export const CREATE_ACCOUNT_PLATFORM_SEGMENT_INACTIVE =
  'text-gray-600 hover:text-gray-900 dark:text-gray-400 dark:hover:text-gray-200'

// --- TokenKey admin UI visuals (merged from adminPlatformVisualStyles.tk) ---

const SOFT_BADGE: Record<string, string> = {
  anthropic: 'bg-orange-100 text-orange-700 dark:bg-orange-900/30 dark:text-orange-400',
  openai: 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-400',
  gemini: 'bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-400',
  antigravity: 'bg-purple-100 text-purple-700 dark:bg-purple-900/30 dark:text-purple-400',
  newapi: 'bg-cyan-100 text-cyan-800 dark:bg-cyan-900/30 dark:text-cyan-300',
}

const LABEL_TEXT: Record<string, string> = {
  anthropic: 'text-orange-600 dark:text-orange-400',
  openai: 'text-emerald-600 dark:text-emerald-400',
  gemini: 'text-blue-600 dark:text-blue-400',
  antigravity: 'text-purple-600 dark:text-purple-400',
  newapi: 'text-cyan-600 dark:text-cyan-400',
}

const TABLE_CELL_BASE =
  'inline-flex items-center gap-1.5 rounded-full px-2.5 py-0.5 text-xs font-medium'

/** Background + text colors for compact platform pills (e.g. channel model tags). */
export function tkAdminPlatformSoftBadgeClass(platform: string): string {
  return SOFT_BADGE[platform] ?? 'bg-gray-100 text-gray-700 dark:bg-gray-900/30 dark:text-gray-400'
}

/** Text color for platform labels next to icons. */
export function tkAdminPlatformLabelTextColor(platform: string): string {
  return LABEL_TEXT[platform] ?? 'text-gray-600 dark:text-gray-400'
}

/** Full class string for the admin groups table platform column. */
export function tkAdminGroupsPlatformTableCellClass(platform: string): string {
  return `${TABLE_CELL_BASE} ${tkAdminPlatformSoftBadgeClass(platform)}`
}
