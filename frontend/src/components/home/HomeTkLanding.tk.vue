<template>
  <!--
    TokenKey-only landing page. Fully isolated from the upstream HomeView.vue
    (which is now a thin wrapper) so upstream merges never touch TK marketing
    markup (CLAUDE.md §5 convergence). All copy is i18n-driven; the TK-only home
    strings live in i18n/tk/home.tk.ts and are merged over the upstream locale.
  -->
  <div
    class="relative flex min-h-screen flex-col overflow-hidden bg-gradient-to-br from-gray-50 via-primary-50/30 to-gray-100 dark:from-dark-950 dark:via-dark-900 dark:to-dark-950"
  >
    <!-- Background Decorations -->
    <div class="pointer-events-none absolute inset-0 overflow-hidden">
      <div
        class="absolute -right-40 -top-40 h-96 w-96 rounded-full bg-primary-400/20 blur-3xl"
      ></div>
      <div
        class="absolute -bottom-40 -left-40 h-96 w-96 rounded-full bg-primary-500/15 blur-3xl"
      ></div>
      <div
        class="absolute left-1/3 top-1/4 h-72 w-72 rounded-full bg-primary-300/10 blur-3xl"
      ></div>
      <div
        class="absolute bottom-1/4 right-1/4 h-64 w-64 rounded-full bg-primary-400/10 blur-3xl"
      ></div>
      <div
        class="absolute inset-0 bg-[linear-gradient(rgba(20,184,166,0.03)_1px,transparent_1px),linear-gradient(90deg,rgba(20,184,166,0.03)_1px,transparent_1px)] bg-[size:64px_64px]"
      ></div>
    </div>

    <!-- Header -->
    <header class="relative z-20 px-6 py-4">
      <nav class="mx-auto flex max-w-6xl items-center justify-between">
        <!-- Logo -->
        <div class="flex items-center">
          <div class="h-10 w-10 overflow-hidden rounded-xl shadow-md">
            <img :src="siteLogo || '/logo.png'" alt="Logo" class="h-full w-full object-contain" />
          </div>
        </div>

        <!-- Nav Actions -->
        <div class="flex items-center gap-3">
          <!-- Language Switcher -->
          <LocaleSwitcher />

          <!-- Pricing Link (TK: docs/approved/user-cold-start.md §2 v1) -->
          <router-link
            v-if="pricingCatalogPublic"
            to="/pricing"
            class="rounded-lg p-2 text-gray-500 transition-colors hover:bg-gray-100 hover:text-gray-700 dark:text-dark-400 dark:hover:bg-dark-800 dark:hover:text-white"
            :title="t('pricing.title')"
            data-tk="cold-start-pricing-link"
          >
            <Icon name="creditCard" size="md" />
          </router-link>

          <!-- Doc Link -->
          <a
            v-if="docUrl"
            :href="docUrl"
            target="_blank"
            rel="noopener noreferrer"
            class="rounded-lg p-2 text-gray-500 transition-colors hover:bg-gray-100 hover:text-gray-700 dark:text-dark-400 dark:hover:bg-dark-800 dark:hover:text-white"
            :title="t('home.viewDocs')"
          >
            <Icon name="book" size="md" />
          </a>

          <!-- Theme Toggle -->
          <button
            @click="toggleTheme"
            class="rounded-lg p-2 text-gray-500 transition-colors hover:bg-gray-100 hover:text-gray-700 dark:text-dark-400 dark:hover:bg-dark-800 dark:hover:text-white"
            :title="isDark ? t('home.switchToLight') : t('home.switchToDark')"
          >
            <Icon v-if="isDark" name="sun" size="md" />
            <Icon v-else name="moon" size="md" />
          </button>

          <!-- Login / Dashboard Button -->
          <router-link
            v-if="isAuthenticated"
            :to="dashboardPath"
            class="inline-flex items-center gap-1.5 rounded-full bg-gray-900 py-1 pl-1 pr-2.5 transition-colors hover:bg-gray-800 dark:bg-gray-800 dark:hover:bg-gray-700"
          >
            <span
              class="flex h-5 w-5 items-center justify-center rounded-full bg-gradient-to-br from-primary-400 to-primary-600 text-[10px] font-semibold text-white"
            >
              {{ userInitial }}
            </span>
            <span class="text-xs font-medium text-white">{{ t('home.dashboard') }}</span>
            <svg
              class="h-3 w-3 text-gray-400"
              fill="none"
              viewBox="0 0 24 24"
              stroke="currentColor"
              stroke-width="2"
            >
              <path
                stroke-linecap="round"
                stroke-linejoin="round"
                d="M4.5 19.5l15-15m0 0H8.25m11.25 0v11.25"
              />
            </svg>
          </router-link>
          <router-link
            v-else
            to="/login"
            class="inline-flex items-center rounded-full bg-gray-900 px-3 py-1 text-xs font-medium text-white transition-colors hover:bg-gray-800 dark:bg-gray-800 dark:hover:bg-gray-700"
          >
            {{ t('home.login') }}
          </router-link>
        </div>
      </nav>
    </header>

    <!-- Main Content -->
    <main class="relative z-10 flex-1 px-6 pb-16 pt-6">
      <div class="mx-auto max-w-6xl">
        <!-- Hero Section - Left/Right Layout -->
        <div class="mb-12 flex flex-col items-center justify-between gap-12 lg:flex-row lg:gap-16">
          <!-- Left: Text Content -->
          <div class="min-w-0 flex-1 text-center lg:text-left">
            <h1
              class="mb-5 text-[2.75rem] font-semibold leading-[1.05] tracking-normal text-gray-950 dark:text-white sm:text-5xl lg:text-[4.5rem]"
            >
              <span
                v-for="line in heroTitleLines"
                :key="line"
                class="block whitespace-nowrap"
              >
                {{ line }}
              </span>
            </h1>
            <p class="mx-auto mb-8 max-w-2xl text-lg leading-8 text-gray-600 dark:text-dark-300 md:text-xl lg:mx-0">
              <span
                v-for="line in heroSubtitleLines"
                :key="line"
                class="block sm:whitespace-nowrap"
              >
                {{ line }}
              </span>
            </p>

            <!-- CTA Button -->
            <div>
              <router-link
                :to="isAuthenticated ? '/quickstart' : '/register?redirect=/quickstart'"
                class="btn btn-primary px-8 py-3 text-base shadow-lg shadow-primary-500/30"
              >
                {{ isAuthenticated ? t('quickstart.title') : t('home.getStarted') }}
                <Icon name="arrowRight" size="md" class="ml-2" :stroke-width="2" />
              </router-link>
            </div>
          </div>

          <!-- Right: Terminal Animation -->
          <div class="flex flex-1 justify-center lg:justify-end">
            <div class="terminal-container">
              <div class="terminal-window">
                <!-- Window header -->
                <div class="terminal-header">
                  <div class="terminal-buttons">
                    <span class="btn-close"></span>
                    <span class="btn-minimize"></span>
                    <span class="btn-maximize"></span>
                  </div>
                  <span class="terminal-title">terminal</span>
                </div>
                <!-- Terminal content: Claude Code CLI against TokenKey -->
                <div class="terminal-body">
                  <div class="code-line line-1">
                    <span class="code-prompt">$</span>
                    <span class="code-cmd">export</span>
                    <span class="code-flag">ANTHROPIC_BASE_URL=</span>
                    <span class="code-url">https://api.tokenkey.dev</span>
                  </div>
                  <div class="code-line line-2">
                    <span class="code-prompt">$</span>
                    <span class="code-cmd">export</span>
                    <span class="code-flag">ANTHROPIC_AUTH_TOKEN=</span>
                    <span class="code-response">sk-tk-••••••••</span>
                  </div>
                  <div class="code-line line-3">
                    <span class="code-prompt">$</span>
                    <span class="code-cmd">claude</span>
                    <span class="code-response">"fix the flaky test"</span>
                  </div>
                  <div class="code-line line-4">
                    <span class="code-success">✓ Done</span>
                    <span class="code-comment">all tests passing</span>
                  </div>
                  <div class="code-line line-5">
                    <span class="code-prompt">$</span>
                    <span class="cursor"></span>
                  </div>
                </div>
              </div>
            </div>
          </div>
        </div>

        <!-- Advantage Cloud: enlarged tags scan the breadth of capabilities -->
        <div class="mb-12 flex flex-wrap items-center justify-center gap-3 md:gap-4">
          <div
            v-for="tag in heroTags"
            :key="tag.key"
            class="inline-flex min-w-[160px] items-center justify-center gap-2.5 rounded-full border border-gray-200/60 bg-white/80 px-6 py-3 shadow-sm backdrop-blur-sm transition-all hover:-translate-y-0.5 hover:border-primary-300 hover:shadow-md dark:border-dark-700/60 dark:bg-dark-800/80 dark:hover:border-primary-700"
          >
            <Icon :name="tag.icon" size="md" class="text-primary-500" />
            <span class="text-base font-semibold text-gray-800 dark:text-dark-100">{{
              t(tag.label)
            }}</span>
          </div>
        </div>

        <!-- Moat Cards: the three differentiators that need proof, not just a label -->
        <div class="mb-16 grid gap-6 md:grid-cols-3">
          <div
            v-for="card in heroCards"
            :key="card.key"
            class="group rounded-2xl border border-gray-200/50 bg-white/60 p-6 backdrop-blur-sm transition-all duration-300 hover:shadow-xl hover:shadow-primary-500/10 dark:border-dark-700/50 dark:bg-dark-800/60"
          >
            <div
              class="mb-4 flex h-12 w-12 items-center justify-center rounded-xl bg-gradient-to-br shadow-lg transition-transform group-hover:scale-110"
              :class="card.gradient"
            >
              <Icon :name="card.icon" size="lg" class="text-white" />
            </div>
            <h3 class="mb-2 text-lg font-semibold text-gray-900 dark:text-white">
              {{ t(card.title) }}
            </h3>
            <p class="text-sm leading-relaxed text-gray-600 dark:text-dark-400">
              {{ t(card.desc) }}
            </p>
          </div>
        </div>

        <!-- Supported Providers -->
        <div class="mb-8 text-center">
          <h2 class="mb-3 text-2xl font-bold text-gray-900 dark:text-white">
            {{ t('home.providers.title') }}
          </h2>
          <p class="text-sm text-gray-600 dark:text-dark-400">
            {{ t('home.providers.description') }}
          </p>
        </div>

        <div class="mb-16 flex flex-wrap items-center justify-center gap-4">
          <div
            v-for="card in HOME_PROVIDER_CARDS"
            :key="card.id"
            class="flex items-center gap-2 rounded-xl border px-5 py-3 backdrop-blur-sm"
            :class="homeProviderCardClass(card.badge)"
          >
            <div
              class="flex h-8 w-8 items-center justify-center rounded-lg bg-gradient-to-br"
              :class="card.gradient"
            >
              <span class="text-xs font-bold text-white">{{ card.glyph }}</span>
            </div>
            <span class="text-sm font-medium text-gray-700 dark:text-dark-200">{{
              t(card.labelKey)
            }}</span>
            <span
              v-if="card.badge !== 'compatible'"
              class="rounded px-1.5 py-0.5 text-[10px] font-medium"
              :class="homeProviderBadgeClass(card.badge)"
              >{{ t(homeProviderBadgeKey(card.badge)) }}</span
            >
            <span
              v-for="mod in card.modalities || []"
              :key="mod"
              class="rounded px-1.5 py-0.5 text-[10px] font-medium"
              :class="homeProviderModalityClass(mod)"
              >{{ homeProviderModalityLabel(mod) }}</span
            >
          </div>
        </div>

        <!-- Problems → Why us (integrated section): pain points lead into the comparison -->
        <div class="mb-8 text-center">
          <h2 class="text-2xl font-bold text-gray-900 dark:text-white">
            {{ t('home.painPoints.title') }}
          </h2>
        </div>
        <div class="mb-12 grid gap-6 sm:grid-cols-2 lg:grid-cols-4">
          <div
            v-for="key in painPointKeys"
            :key="key"
            class="rounded-2xl border border-gray-200/50 bg-white/60 p-6 backdrop-blur-sm dark:border-dark-700/50 dark:bg-dark-800/60"
          >
            <h3 class="mb-2 text-base font-semibold text-gray-900 dark:text-white">
              {{ t(`home.painPoints.items.${key}.title`) }}
            </h3>
            <p class="text-sm leading-relaxed text-gray-600 dark:text-dark-400">
              {{ t(`home.painPoints.items.${key}.desc`) }}
            </p>
          </div>
        </div>

        <!-- Why Choose Us: official subscription vs us -->
        <div class="mb-8 text-center">
          <h2 class="text-2xl font-bold text-gray-900 dark:text-white">
            {{ t('home.comparison.title') }}
          </h2>
        </div>
        <div
          class="mb-16 overflow-x-auto rounded-2xl border border-gray-200/50 bg-white/60 backdrop-blur-sm dark:border-dark-700/50 dark:bg-dark-800/60"
        >
          <table class="w-full min-w-[600px] text-left text-sm">
            <thead>
              <tr class="border-b border-gray-200/50 dark:border-dark-700/50">
                <th class="px-5 py-4 font-medium text-gray-500 dark:text-dark-400">
                  {{ t('home.comparison.headers.feature') }}
                </th>
                <th class="px-5 py-4 font-medium text-gray-500 dark:text-dark-400">
                  {{ t('home.comparison.headers.official') }}
                </th>
                <th class="px-5 py-4 font-medium text-gray-500 dark:text-dark-400">
                  {{ t('home.comparison.headers.thirdParty') }}
                </th>
                <th class="px-5 py-4 font-semibold text-primary-600 dark:text-primary-400">
                  {{ t('home.comparison.headers.us') }}
                </th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="(key, i) in comparisonRows"
                :key="key"
                :class="i < comparisonRows.length - 1 ? 'border-b border-gray-200/50 dark:border-dark-700/50' : ''"
              >
                <td class="px-5 py-4 font-medium text-gray-900 dark:text-white">
                  {{ t(`home.comparison.items.${key}.feature`) }}
                </td>
                <td class="px-5 py-4 text-center text-gray-500 dark:text-dark-400">
                  {{ t(`home.comparison.items.${key}.official`) }}
                </td>
                <td class="px-5 py-4 text-center text-gray-500 dark:text-dark-400">
                  {{ t(`home.comparison.items.${key}.thirdParty`) }}
                </td>
                <td class="px-5 py-4 text-center font-medium text-primary-600 dark:text-primary-400">
                  {{ t(`home.comparison.items.${key}.us`) }}
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <!-- CTA band -->
        <div
          class="rounded-2xl border border-primary-200/60 bg-gradient-to-br from-primary-50 to-white p-10 text-center backdrop-blur-sm dark:border-primary-800/60 dark:from-dark-800/80 dark:to-dark-900/60"
        >
          <div class="mb-4">
            <router-link
              :to="isAuthenticated ? '/quickstart' : '/register?redirect=/quickstart'"
              class="btn btn-primary px-8 py-3 text-lg font-semibold shadow-lg shadow-primary-500/30"
            >
              {{ t('home.cta.title') }}
              <Icon name="arrowRight" size="md" class="ml-2" :stroke-width="2" />
            </router-link>
          </div>
          <p class="text-base text-gray-600 dark:text-dark-300">
            {{ t('home.cta.description') }}
          </p>
        </div>
      </div>
    </main>

    <!-- Footer -->
    <footer class="relative z-10 border-t border-gray-200/50 px-6 py-8 dark:border-dark-800/50">
      <div
        class="mx-auto flex max-w-6xl flex-col items-center justify-center gap-4 text-center sm:flex-row sm:text-left"
      >
        <p class="text-sm text-gray-500 dark:text-dark-400">
          &copy; {{ currentYear }} {{ siteName }}. {{ t('home.footer.allRightsReserved') }}
        </p>
        <div class="flex items-center gap-4">
          <a
            v-if="docUrl"
            :href="docUrl"
            target="_blank"
            rel="noopener noreferrer"
            class="text-sm text-gray-500 transition-colors hover:text-gray-700 dark:text-dark-400 dark:hover:text-white"
          >
            {{ t('home.docs') }}
          </a>
        </div>
      </div>
    </footer>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAuthStore, useAppStore } from '@/stores'
import LocaleSwitcher from '@/components/common/LocaleSwitcher.vue'
import Icon from '@/components/icons/Icon.vue'
import {
  HOME_PROVIDER_CARDS,
  homeProviderCardClass,
  homeProviderBadgeClass,
  homeProviderBadgeKey,
  homeProviderModalityLabel,
  homeProviderModalityClass,
} from '@/constants/homeProviders.tk'

const { t } = useI18n()

const heroTitleLines = computed(() => {
  const title = t('home.hero.title')
  if (title === '每一次调用，都是官方品质。') {
    return ['每一次调用，', '都是官方品质。']
  }
  return [title]
})

const heroSubtitleLines = computed(() => {
  const subtitle = t('home.hero.subtitle')
  if (subtitle === '一个 API Key，所有主流 AI 模型。文本、图像、视频。订阅配额，费用可预测。') {
    return ['一个 API Key，所有主流 AI 模型。', '文本、图像、视频。订阅配额，费用可预测。']
  }
  return [subtitle]
})

// Enlarged advantage tags — these carry the core advantages now that the
// three feature cards are gone. Each maps to a home.tags.* i18n key.
const heroTags = [
  { key: 'native-cc', icon: 'terminal', label: 'home.tags.subscriptionToApi' },
  { key: 'fidelity', icon: 'checkCircle', label: 'home.tags.nativeFidelity' },
  { key: 'failover', icon: 'arrowsUpDown', label: 'home.tags.failover' },
  { key: 'multi-platform', icon: 'grid', label: 'home.tags.multiPlatform' },
  { key: 'sticky', icon: 'link', label: 'home.tags.stickySession' },
  { key: 'quota', icon: 'shield', label: 'home.tags.quotaControl' },
] as const

// Moat cards — the three differentiators that need a proof sentence, not a tag.
const heroCards = [
  {
    key: 'native',
    icon: 'terminal',
    gradient: 'from-primary-500 to-primary-600 shadow-primary-500/30',
    title: 'home.cards.native.title',
    desc: 'home.cards.native.desc',
  },
  {
    key: 'stability',
    icon: 'server',
    gradient: 'from-blue-500 to-blue-600 shadow-blue-500/30',
    title: 'home.cards.stability.title',
    desc: 'home.cards.stability.desc',
  },
  {
    key: 'billing',
    icon: 'creditCard',
    gradient: 'from-purple-500 to-purple-600 shadow-purple-500/30',
    title: 'home.cards.billing.title',
    desc: 'home.cards.billing.desc',
  },
] as const

// Static iteration keys for the pain-point / comparison i18n blocks.
const painPointKeys = ['expensive', 'complex', 'unstable', 'noControl'] as const
const comparisonRows = ['unified', 'quota', 'quality', 'multimodal', 'monitoring'] as const

const authStore = useAuthStore()
const appStore = useAppStore()

// Site settings - directly from appStore (already initialized from injected config)
const siteName = computed(() => appStore.cachedPublicSettings?.site_name || appStore.siteName || 'TokenKey')
const siteLogo = computed(() => appStore.cachedPublicSettings?.site_logo || appStore.siteLogo || '')
const docUrl = computed(() => appStore.cachedPublicSettings?.doc_url || appStore.docUrl || '')
// TK cold-start (US-028): public pricing entry visibility. Defaults to true so a
// brand-new install without the row in DB still surfaces the link; flips off
// only when an admin explicitly disables `pricing_catalog_public`.
const pricingCatalogPublic = computed(() => {
  const v = appStore.cachedPublicSettings?.pricing_catalog_public
  return v === undefined ? true : v
})

// Theme
const isDark = ref(document.documentElement.classList.contains('dark'))

// Auth state
const isAuthenticated = computed(() => authStore.isAuthenticated)
const isAdmin = computed(() => authStore.isAdmin)
const dashboardPath = computed(() => (isAdmin.value ? '/admin/dashboard' : '/dashboard'))
const userInitial = computed(() => {
  const user = authStore.user
  if (!user || !user.email) return ''
  return user.email.charAt(0).toUpperCase()
})

// Current year for footer
const currentYear = computed(() => new Date().getFullYear())

// Toggle theme
function toggleTheme() {
  isDark.value = !isDark.value
  document.documentElement.classList.toggle('dark', isDark.value)
  localStorage.setItem('theme', isDark.value ? 'dark' : 'light')
}

// Initialize theme
function initTheme() {
  const savedTheme = localStorage.getItem('theme')
  if (
    savedTheme === 'dark' ||
    (!savedTheme && window.matchMedia('(prefers-color-scheme: dark)').matches)
  ) {
    isDark.value = true
    document.documentElement.classList.add('dark')
  }
}

onMounted(() => {
  initTheme()
  authStore.checkAuth()
})
</script>

<style scoped>
/* Terminal Container */
.terminal-container {
  position: relative;
  display: inline-block;
}

/* Terminal Window */
.terminal-window {
  width: 540px;
  max-width: 100%;
  background: linear-gradient(145deg, #1e293b 0%, #0f172a 100%);
  border-radius: 14px;
  box-shadow:
    0 25px 50px -12px rgba(0, 0, 0, 0.4),
    0 0 0 1px rgba(255, 255, 255, 0.1),
    inset 0 1px 0 rgba(255, 255, 255, 0.1);
  overflow: hidden;
  transform: perspective(1000px) rotateX(2deg) rotateY(-2deg);
  transition: transform 0.3s ease;
}

.terminal-window:hover {
  transform: perspective(1000px) rotateX(0deg) rotateY(0deg) translateY(-4px);
}

/* Terminal Header */
.terminal-header {
  display: flex;
  align-items: center;
  padding: 12px 16px;
  background: rgba(30, 41, 59, 0.8);
  border-bottom: 1px solid rgba(255, 255, 255, 0.05);
}

.terminal-buttons {
  display: flex;
  gap: 8px;
}

.terminal-buttons span {
  width: 12px;
  height: 12px;
  border-radius: 50%;
}

.btn-close {
  background: #ef4444;
}
.btn-minimize {
  background: #eab308;
}
.btn-maximize {
  background: #22c55e;
}

.terminal-title {
  flex: 1;
  text-align: center;
  font-size: 12px;
  font-family: ui-monospace, monospace;
  color: #64748b;
  margin-right: 52px;
}

/* Terminal Body */
.terminal-body {
  padding: 20px 24px;
  font-family: ui-monospace, 'Fira Code', monospace;
  font-size: 14px;
  line-height: 2;
}

.code-line {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  opacity: 0;
  animation: line-appear 0.5s ease forwards;
}

.line-1 {
  animation-delay: 0.3s;
}
.line-2 {
  animation-delay: 0.9s;
}
.line-3 {
  animation-delay: 1.6s;
}
.line-4 {
  animation-delay: 2.3s;
}
.line-5 {
  animation-delay: 3s;
}

@keyframes line-appear {
  from {
    opacity: 0;
    transform: translateY(5px);
  }
  to {
    opacity: 1;
    transform: translateY(0);
  }
}

.code-prompt {
  color: #22c55e;
  font-weight: bold;
}
.code-cmd {
  color: #38bdf8;
}
.code-flag {
  color: #a78bfa;
}
.code-url {
  color: #14b8a6;
}
.code-comment {
  color: #64748b;
  font-style: italic;
}
.code-success {
  color: #22c55e;
  background: rgba(34, 197, 94, 0.15);
  padding: 2px 8px;
  border-radius: 4px;
  font-weight: 600;
}
.code-response {
  color: #fbbf24;
}

/* Blinking Cursor */
.cursor {
  display: inline-block;
  width: 8px;
  height: 16px;
  background: #22c55e;
  animation: blink 1s step-end infinite;
}

@keyframes blink {
  0%,
  50% {
    opacity: 1;
  }
  51%,
  100% {
    opacity: 0;
  }
}

/* Dark mode adjustments */
:deep(.dark) .terminal-window {
  box-shadow:
    0 25px 50px -12px rgba(0, 0, 0, 0.6),
    0 0 0 1px rgba(20, 184, 166, 0.2),
    0 0 40px rgba(20, 184, 166, 0.1),
    inset 0 1px 0 rgba(255, 255, 255, 0.1);
}
</style>
