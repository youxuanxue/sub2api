// TokenKey-only home-page provider cards.
//
// Upstream HomeView.vue stays template + v-for; this module owns the TK provider
// list and badge styling so the upstream-shaped .vue keeps a minimal diff
// (CLAUDE.md §5: views stay template+wiring, constants/*.tk.ts own TK-only maps).
//
// Honesty rule: only cards we actually serve carry the `supported` badge.
// `compatible` is intentionally softer ("可接入") — it advertises the
// OpenAI-compatible upstream surface without claiming each one is probe-verified.

/** supported = 我们实测在服务的一级/具体平台; compatible = 泛化「可接入」上游, 措辞更软。 */
export type HomeProviderBadge = 'supported' | 'compatible'

/** Modality capabilities beyond text that a provider supports. */
export type HomeProviderModality = 'image' | 'video'

export interface HomeProviderCard {
  /** stable key, also used as the v-for :key */
  id: string
  /** i18n path under home.providers.* for the display label */
  labelKey: string
  /** single glyph rendered inside the gradient logo square */
  glyph: string
  /** tailwind gradient classes for the logo square (from-… to-…) */
  gradient: string
  /** badge semantics → drives card chrome + badge text */
  badge: HomeProviderBadge
  /** optional second line (e.g. protocol-matrix tagline on the compat card) */
  taglineKey?: string
  /** protocol surface pills shown on the compat card */
  protocolTagKeys?: string[]
  /** additional modalities beyond text (image gen, video gen) */
  modalities?: HomeProviderModality[]
}

export const HOME_PROVIDER_CARDS: HomeProviderCard[] = [
  { id: 'claude', labelKey: 'home.providers.claude', glyph: 'C', gradient: 'from-orange-400 to-orange-500', badge: 'supported' },
  { id: 'gpt', labelKey: 'home.providers.gpt', glyph: 'G', gradient: 'from-green-500 to-green-600', badge: 'supported', modalities: ['image'] },
  { id: 'gemini', labelKey: 'home.providers.gemini', glyph: 'G', gradient: 'from-blue-500 to-blue-600', badge: 'supported', modalities: ['image', 'video'] },
  { id: 'kiro', labelKey: 'home.providers.kiro', glyph: 'K', gradient: 'from-indigo-500 to-indigo-600', badge: 'supported' },
  { id: 'qwen', labelKey: 'home.providers.qwen', glyph: 'Q', gradient: 'from-violet-500 to-purple-600', badge: 'supported', modalities: ['image'] },
  // 协议矩阵卡: 100+ 模型 + 客户端常见 API 形态，措辞准确、不夸大 OpenAI-compat 数量。
  {
    id: 'compat',
    labelKey: 'home.providers.compatTitle',
    taglineKey: 'home.providers.compatTagline',
    protocolTagKeys: [
      'home.providers.compatProtocolMessages',
      'home.providers.compatProtocolChat',
      'home.providers.compatProtocolResponses',
    ],
    glyph: '⎔',
    gradient: 'from-primary-500 to-teal-600',
    badge: 'compatible',
  },
]

/** 卡片外壳: supported 用 primary ring 高亮; compatible（协议矩阵）同样用 primary 强调差异化。 */
export function homeProviderCardClass(badge: HomeProviderBadge): string {
  return badge === 'compatible'
    ? 'border-primary-200/80 bg-white/70 ring-1 ring-primary-500/15 dark:border-primary-900/50 dark:bg-dark-800/70'
    : 'border-primary-200 bg-white/60 ring-1 ring-primary-500/20 dark:border-primary-800 dark:bg-dark-800/60'
}

/** badge pill: supported 绿; compatible 灰。 */
export function homeProviderBadgeClass(badge: HomeProviderBadge): string {
  return badge === 'compatible'
    ? 'bg-gray-100 text-gray-500 dark:bg-dark-700 dark:text-dark-400'
    : 'bg-primary-100 text-primary-600 dark:bg-primary-900/30 dark:text-primary-400'
}

/** badge 文案 i18n key by type。 */
export function homeProviderBadgeKey(badge: HomeProviderBadge): string {
  return badge === 'compatible' ? 'home.providers.compatible' : 'home.providers.supported'
}

/** Short display label for a modality tag (not i18n — too short to warrant keys). */
export function homeProviderModalityLabel(modality: HomeProviderModality): string {
  const labels: Record<HomeProviderModality, string> = { image: 'IMG', video: 'VID' }
  return labels[modality]
}

/** Tailwind classes for modality tag pills. */
export function homeProviderModalityClass(modality: HomeProviderModality): string {
  return modality === 'video'
    ? 'bg-purple-100 text-purple-600 dark:bg-purple-900/30 dark:text-purple-400'
    : 'bg-amber-100 text-amber-600 dark:bg-amber-900/30 dark:text-amber-400'
}
