<template>
  <!--
    TokenKey-only landing page. Fully isolated from the upstream HomeView.vue
    (which is now a thin wrapper) so upstream merges never touch TK marketing
    markup (CLAUDE.md §5 convergence). All copy is i18n-driven; the TK-only home
    strings live in i18n/tk/home.tk.ts and are merged over the upstream locale.
  -->
  <div
    class="relative flex min-h-screen flex-col overflow-hidden bg-gradient-to-br from-gray-50 via-primary-50/30 to-gray-100 dark:from-dark-950 dark:via-dark-900 dark:to-dark-950"
    :data-home-profile="profile"
  >
    <!-- Background Decorations -->
    <div v-if="!isChinaExport" class="pointer-events-none absolute inset-0 overflow-hidden">
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
        <div class="flex items-center gap-3">
          <div class="h-10 w-10 overflow-hidden rounded-xl shadow-md">
            <img :src="siteLogo" :alt="brandName" class="h-full w-full object-contain" />
          </div>
          <span v-if="isChinaExport" class="text-base font-semibold text-gray-950 dark:text-white">
            {{ brandName }}
          </span>
        </div>

        <!-- Nav Actions -->
        <div class="flex items-center gap-3">
          <!-- Language Switcher -->
          <LocaleSwitcher />

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

          <router-link
            v-if="!isChinaExport && showModelPlazaEntry"
            to="/model-plaza"
            class="inline-flex items-center gap-1.5 rounded-lg p-2 text-sm text-gray-500 transition-colors hover:bg-gray-100 hover:text-gray-700 dark:text-dark-400 dark:hover:bg-dark-800 dark:hover:text-white"
            :title="t('nav.modelPlaza')"
          >
            <Icon name="grid" size="md" />
            <span class="hidden sm:inline">{{ t('nav.modelPlaza') }}</span>
          </router-link>

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
          <a
            v-if="isChinaExport && isAuthenticated"
            :href="absoluteProductUrl(dashboardPath)"
            class="inline-flex items-center gap-1.5 rounded-full bg-gray-900 py-1 pl-1 pr-2.5 transition-colors hover:bg-gray-800 dark:bg-gray-800 dark:hover:bg-gray-700"
          >
            <span
              class="flex h-5 w-5 items-center justify-center rounded-full bg-primary-600 text-[10px] font-semibold text-white"
            >
              {{ userInitial }}
            </span>
            <span class="text-xs font-medium text-white">{{ t('home.dashboard') }}</span>
            <Icon name="arrowRight" size="xs" class="text-gray-400" />
          </a>
          <a
            v-else-if="isChinaExport"
            :href="absoluteProductUrl('/login')"
            class="inline-flex items-center rounded-full bg-gray-900 px-3 py-1 text-xs font-medium text-white transition-colors hover:bg-gray-800 dark:bg-gray-800 dark:hover:bg-gray-700"
          >
            {{ t('home.login') }}
          </a>
          <router-link
            v-else-if="isAuthenticated"
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
    <main v-if="!isChinaExport" class="relative z-10 flex-1 px-6 pb-16 pt-6">
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
                class="block [overflow-wrap:anywhere]"
              >
                {{ line }}
              </span>
            </h1>
            <p class="mx-auto mb-8 max-w-2xl text-lg leading-8 text-gray-600 dark:text-dark-300 md:text-xl lg:mx-0">
              <span
                v-for="line in heroSubtitleLines"
                :key="line"
                class="block"
              >
                {{ line }}
              </span>
            </p>

            <!-- Free Trial Badge -->
            <div class="mb-6">
              <span
                class="inline-flex items-center gap-1.5 rounded-full border border-primary-200 bg-primary-50/80 px-4 py-1.5 text-sm font-medium text-primary-700 dark:border-primary-800 dark:bg-primary-900/30 dark:text-primary-300"
              >
                {{ t('home.freeTrial.badge') }}
              </span>
            </div>

            <!-- CTA Buttons -->
            <div class="flex flex-wrap items-center justify-center gap-3 lg:justify-start">
              <router-link
                :to="isAuthenticated ? '/quickstart' : '/register?redirect=/quickstart'"
                class="btn btn-primary px-8 py-3 text-base shadow-lg shadow-primary-500/30"
              >
                {{ isAuthenticated ? t('quickstart.title') : t('home.getStarted') }}
                <Icon name="arrowRight" size="md" class="ml-2" :stroke-width="2" />
              </router-link>
              <router-link
                to="/models"
                class="btn btn-secondary px-6 py-3 text-base"
                data-tk="cold-start-models-hub-hero"
              >
                {{ t('models.title') }}
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
              class="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-gradient-to-br"
              :class="card.gradient"
            >
              <span class="text-xs font-bold text-white">{{ card.glyph }}</span>
            </div>
            <div class="flex min-w-0 flex-col gap-1">
              <div class="flex flex-wrap items-center gap-x-2 gap-y-0.5">
                <span class="text-sm font-medium text-gray-700 dark:text-dark-200">{{
                  t(card.labelKey)
                }}</span>
                <span
                  v-if="card.taglineKey"
                  class="text-xs font-semibold tracking-wide text-primary-600 dark:text-primary-400"
                >
                  {{ t(card.taglineKey) }}
                </span>
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
              <div v-if="card.protocolTagKeys?.length" class="flex flex-wrap gap-1">
                <span
                  v-for="tagKey in card.protocolTagKeys"
                  :key="tagKey"
                  class="rounded border border-primary-200/80 bg-primary-50/80 px-1.5 py-0.5 font-mono text-[10px] font-medium text-primary-700 dark:border-primary-800/60 dark:bg-primary-950/40 dark:text-primary-300"
                >
                  {{ t(tagKey) }}
                </span>
              </div>
            </div>
          </div>
        </div>

        <!-- Model marketplace CTA (below providers) -->
        <div class="mb-16 text-center">
          <router-link
            to="/models"
            class="inline-flex items-center gap-2 rounded-full border border-primary-200 bg-primary-50 px-5 py-2.5 text-sm font-medium text-primary-700 transition-all hover:bg-primary-100 hover:shadow-md dark:border-primary-800 dark:bg-primary-900/30 dark:text-primary-300 dark:hover:bg-primary-900/50"
            data-tk="cold-start-models-hub-link"
          >
            {{ t('models.title') }}
            <svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2">
              <path stroke-linecap="round" stroke-linejoin="round" d="M13.5 4.5L21 12m0 0l-7.5 7.5M21 12H3" />
            </svg>
          </router-link>
        </div>

        <!-- Use Cases -->
        <div class="mb-8 text-center">
          <h2 class="mb-3 text-2xl font-bold text-gray-900 dark:text-white">
            {{ t('home.useCases.title') }}
          </h2>
          <p class="text-sm text-gray-600 dark:text-dark-400">
            {{ t('home.useCases.subtitle') }}
          </p>
        </div>
        <div class="mb-16 grid gap-6 md:grid-cols-3">
          <div
            v-for="uc in useCaseCards"
            :key="uc.key"
            class="group rounded-2xl border border-gray-200/50 bg-white/60 p-6 backdrop-blur-sm transition-all duration-300 hover:shadow-xl hover:shadow-primary-500/10 dark:border-dark-700/50 dark:bg-dark-800/60"
          >
            <div
              class="mb-4 flex h-12 w-12 items-center justify-center rounded-xl bg-gradient-to-br shadow-lg transition-transform group-hover:scale-110"
              :class="uc.gradient"
            >
              <Icon :name="uc.icon" size="lg" class="text-white" />
            </div>
            <h3 class="mb-2 text-lg font-semibold text-gray-900 dark:text-white">
              {{ t(uc.title) }}
            </h3>
            <p class="text-sm leading-relaxed text-gray-600 dark:text-dark-400">
              {{ t(uc.desc) }}
            </p>
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

        <!-- FAQ Accordion -->
        <div class="mb-8 text-center">
          <h2 class="text-2xl font-bold text-gray-900 dark:text-white">
            {{ t('home.faq.title') }}
          </h2>
        </div>
        <div class="mb-16 space-y-3">
          <div
            v-for="key in faqKeys"
            :key="key"
            class="overflow-hidden rounded-xl border border-gray-200/50 bg-white/60 backdrop-blur-sm dark:border-dark-700/50 dark:bg-dark-800/60"
          >
            <button
              class="flex w-full items-center justify-between px-6 py-4 text-left transition-colors hover:bg-gray-50/80 dark:hover:bg-dark-700/40"
              @click="toggleFaq(key)"
            >
              <span class="text-sm font-medium text-gray-900 dark:text-white">
                {{ t(`home.faq.items.${key}.q`) }}
              </span>
              <Icon
                name="chevronDown"
                size="sm"
                class="flex-shrink-0 text-gray-400 transition-transform duration-200 dark:text-dark-500"
                :class="{ 'rotate-180': openFaqItems[key] }"
              />
            </button>
            <div
              v-show="openFaqItems[key]"
              class="border-t border-gray-200/50 px-6 py-4 dark:border-dark-700/50"
            >
              <p class="text-sm leading-relaxed text-gray-600 dark:text-dark-400">
                {{ t(`home.faq.items.${key}.a`) }}
              </p>
            </div>
          </div>
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

    <main v-else class="relative z-10 flex-1" data-testid="china-export-home">
      <section class="border-b border-gray-200/50 px-5 pb-6 pt-6 dark:border-dark-800/50 sm:px-8 sm:pb-14 sm:pt-8 lg:px-12 lg:pb-20 lg:pt-12">
        <div class="mx-auto grid max-w-7xl items-center gap-6 sm:gap-10 lg:grid-cols-[minmax(0,0.88fr)_minmax(0,1.12fr)] lg:gap-14">
          <div class="max-w-2xl">
            <p class="mb-5 text-sm font-semibold uppercase text-primary-600 dark:text-primary-400">
              {{ t('home.chinaExport.eyebrow') }}
            </p>
            <h1
              class="break-keep text-4xl font-semibold leading-[1.05] tracking-normal text-gray-950 dark:text-white sm:text-5xl"
              :class="locale === 'zh' ? 'lg:text-6xl' : 'lg:text-7xl'"
            >
              {{ t('home.chinaExport.heroTitle') }}
            </h1>
            <p class="mt-6 max-w-xl text-lg leading-8 text-gray-600 dark:text-gray-300 sm:text-xl">
              {{ t('home.chinaExport.heroSubtitle') }}
            </p>
            <div class="mt-8 flex flex-wrap items-center gap-3">
              <a
                :href="primaryCtaUrl"
                class="btn btn-primary px-6 py-3 text-base shadow-lg shadow-primary-500/30"
                data-testid="china-export-primary-cta"
              >
                {{ isAuthenticated ? t('home.chinaExport.openQuickstart') : t('home.chinaExport.startFree') }}
                <Icon name="arrowRight" size="md" class="ml-2" :stroke-width="2" />
              </a>
              <a
                :href="absoluteProductUrl('/models')"
                class="btn btn-secondary px-6 py-3 text-base"
              >
                {{ t('home.chinaExport.browseModels') }}
              </a>
            </div>
            <p class="mt-4 text-sm text-gray-500 dark:text-gray-400">
              {{ t('home.chinaExport.noCard') }}
            </p>
          </div>

          <figure class="overflow-hidden rounded-lg bg-dark-950 shadow-2xl shadow-primary-950/20" data-testid="seedance-proof">
            <div class="relative aspect-video overflow-hidden">
              <video
                ref="seedanceVideo"
                class="h-full w-full object-cover motion-reduce:hidden"
                :src="seedanceProof.videoUrl"
                :poster="seedanceProof.posterUrl"
                autoplay
                muted
                loop
                playsinline
                controls
                preload="metadata"
                data-testid="seedance-proof-video"
              ></video>
              <img
                :src="seedanceProof.posterUrl"
                :alt="t('home.chinaExport.proofAlt')"
                class="hidden h-full w-full object-cover motion-reduce:block"
                data-testid="seedance-proof-poster"
              />
              <div class="pointer-events-none absolute left-4 top-4 rounded bg-black/75 px-2.5 py-1 text-xs font-medium text-white backdrop-blur-sm">
                {{ t('home.chinaExport.proofBadge') }}
              </div>
            </div>
            <figcaption class="flex flex-col gap-1 px-4 py-3 text-sm text-white/80 sm:flex-row sm:items-center sm:justify-between">
              <span>{{ t('home.chinaExport.proofCaption') }}</span>
              <a
                :href="seedanceProof.sourceUrl"
                target="_blank"
                rel="noopener noreferrer"
                class="pointer-events-auto text-white underline decoration-white/40 underline-offset-4 hover:decoration-white"
              >
                {{ t('home.chinaExport.proofSource') }}
              </a>
            </figcaption>
          </figure>
        </div>
      </section>

      <section class="px-5 pb-16 pt-6 sm:px-8 sm:pt-12 lg:px-12 lg:py-20" aria-labelledby="china-models-title">
        <div class="mx-auto max-w-7xl">
          <div class="max-w-2xl">
            <p class="text-sm font-semibold uppercase text-primary-600 dark:text-primary-400" data-testid="china-models-eyebrow">{{ t('home.chinaExport.modelsEyebrow') }}</p>
            <h2 id="china-models-title" class="mt-3 text-3xl font-semibold tracking-normal text-gray-950 dark:text-white sm:text-4xl">
              {{ t('home.chinaExport.modelsTitle') }}
            </h2>
            <p class="mt-4 text-base leading-7 text-gray-600 dark:text-gray-300">
              {{ t('home.chinaExport.modelsSubtitle') }}
            </p>
          </div>

          <div class="mt-10 grid gap-3 md:grid-cols-2 lg:grid-cols-3" data-testid="china-model-list">
            <div
              v-for="(model, index) in chinaModelCards"
              :key="model.name"
              class="flex min-h-[132px] items-start gap-4 rounded-lg border border-gray-200/50 bg-white/60 p-5 backdrop-blur-sm dark:border-dark-700/50 dark:bg-dark-800/60"
              :data-model-order="index + 1"
            >
              <span class="flex h-10 w-10 shrink-0 items-center justify-center rounded-md text-sm font-bold text-white" :class="model.color">
                {{ model.glyph }}
              </span>
              <div class="min-w-0">
                <div class="flex flex-wrap items-center gap-2">
                  <h3 class="text-base font-semibold text-gray-950 dark:text-white">{{ model.name }}</h3>
                  <span v-if="index < 2" class="rounded bg-primary-50 px-2 py-0.5 text-xs font-medium text-primary-700 dark:bg-primary-900/30 dark:text-primary-300">
                    {{ t('home.chinaExport.featured') }}
                  </span>
                </div>
                <p class="mt-2 text-sm leading-6 text-gray-600 dark:text-gray-300">{{ t(model.descriptionKey) }}</p>
              </div>
            </div>
          </div>
        </div>
      </section>

      <section class="border-y border-gray-200/50 bg-white/60 px-5 py-16 backdrop-blur-sm dark:border-dark-800/50 dark:bg-dark-800/60 sm:px-8 lg:px-12 lg:py-20" aria-labelledby="verify-key-title">
        <div class="mx-auto grid max-w-7xl gap-10 lg:grid-cols-[minmax(0,0.72fr)_minmax(0,1.28fr)] lg:gap-16">
          <div>
            <p class="text-sm font-semibold uppercase text-primary-600 dark:text-primary-400">{{ t('home.chinaExport.verifyEyebrow') }}</p>
            <h2 id="verify-key-title" class="mt-3 text-3xl font-semibold tracking-normal text-gray-950 dark:text-white sm:text-4xl">
              {{ t('home.chinaExport.verifyTitle') }}
            </h2>
            <p class="mt-4 text-base leading-7 text-gray-600 dark:text-gray-300">
              {{ t('home.chinaExport.verifyDescription') }}
            </p>
            <a :href="primaryCtaUrl" class="mt-7 inline-flex items-center font-semibold text-primary-600 hover:underline dark:text-primary-400">
              {{ t('home.chinaExport.verifyCta') }}
              <Icon name="arrowRight" size="sm" class="ml-2" />
            </a>
          </div>

          <div class="terminal-container w-full max-w-[540px] lg:justify-self-end" data-testid="china-export-terminal">
            <div class="terminal-window">
              <div class="terminal-header">
                <div class="terminal-buttons">
                  <span class="btn-close"></span>
                  <span class="btn-minimize"></span>
                  <span class="btn-maximize"></span>
                </div>
                <span class="terminal-title">terminal</span>
                <button
                  type="button"
                  class="terminal-copy-button"
                  :title="codeCopied ? t('home.chinaExport.copied') : t('home.chinaExport.copyCode')"
                  @click="copyQuickstart"
                >
                  <Icon :name="codeCopied ? 'check' : 'copy'" size="sm" />
                </button>
              </div>
              <pre class="terminal-body terminal-command-body overflow-x-auto"><code>{{ deepseekCurl }}</code></pre>
            </div>
          </div>
        </div>
      </section>

      <section class="px-5 py-16 sm:px-8 lg:px-12 lg:py-20" aria-labelledby="china-faq-title">
        <div class="mx-auto grid max-w-7xl gap-10 lg:grid-cols-[minmax(0,0.65fr)_minmax(0,1.35fr)] lg:gap-16">
          <div>
            <p class="text-sm font-semibold uppercase text-primary-600 dark:text-primary-400">{{ t('home.chinaExport.faqEyebrow') }}</p>
            <h2 id="china-faq-title" class="mt-3 text-3xl font-semibold tracking-normal text-gray-950 dark:text-white sm:text-4xl">
              {{ t('home.chinaExport.faqTitle') }}
            </h2>
            <a :href="primaryCtaUrl" class="mt-7 inline-flex items-center font-semibold text-primary-600 hover:underline dark:text-primary-400">
              {{ t('home.chinaExport.startFree') }}
              <Icon name="arrowRight" size="sm" class="ml-2" />
            </a>
            <p class="mt-3 max-w-sm text-sm leading-6 text-gray-500 dark:text-gray-400">
              {{ t('home.chinaExport.creditDisclaimer') }}
            </p>
          </div>

          <div class="divide-y divide-gray-200/50 border-y border-gray-200/50 dark:divide-dark-700/50 dark:border-dark-700/50">
            <div v-for="key in chinaFaqKeys" :key="key">
              <button
                type="button"
                class="flex min-h-[64px] w-full items-center justify-between gap-4 py-4 text-left"
                :aria-expanded="Boolean(openChinaFaqItems[key])"
                @click="toggleChinaFaq(key)"
              >
                <span class="font-medium text-gray-950 dark:text-white">{{ t(`home.chinaExport.faq.${key}.q`) }}</span>
                <Icon
                  name="chevronDown"
                  size="sm"
                  class="shrink-0 transition-transform"
                  :class="{ 'rotate-180': openChinaFaqItems[key] }"
                />
              </button>
              <p v-show="openChinaFaqItems[key]" class="max-w-3xl pb-5 pr-10 text-sm leading-6 text-gray-600 dark:text-gray-300">
                {{ t(`home.chinaExport.faq.${key}.a`) }}
              </p>
            </div>
          </div>
        </div>
      </section>
    </main>

    <!-- Footer -->
    <footer class="relative z-10 border-t border-gray-200/50 px-6 py-8 dark:border-dark-800/50">
      <div
        class="mx-auto flex max-w-6xl flex-col items-center justify-center gap-4 text-center sm:flex-row sm:text-left"
      >
        <p class="text-sm text-gray-500 dark:text-dark-400">
          &copy; {{ currentYear }} {{ brandName }}. {{ t('home.footer.allRightsReserved') }}
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
import { ref, reactive, computed } from 'vue'
import { useI18n } from 'vue-i18n'
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
import { PRODUCT_ORIGIN, type HomepageProfile } from '@/features/home/marketProfile.tk'
import { useHomeShell } from '@/features/home/useHomeShell.tk'

const props = withDefaults(defineProps<{
  profile?: HomepageProfile
}>(), {
  profile: 'current',
})

const { t, locale } = useI18n()
const isChinaExport = computed(() => props.profile === 'china-export')
const brandName = 'TokenKey'

const CHINA_EXPORT_QUICKSTART = '/quickstart?model=deepseek-chat&protocol=openai'

function absoluteProductUrl(path: string): string {
  return new URL(path, PRODUCT_ORIGIN).toString()
}

const primaryCtaUrl = computed(() =>
  isAuthenticated.value
    ? absoluteProductUrl(CHINA_EXPORT_QUICKSTART)
    : `${PRODUCT_ORIGIN}/register?redirect=${encodeURIComponent(CHINA_EXPORT_QUICKSTART)}`,
)

const seedanceProof = {
  videoUrl: '/seedance-2-5-official-showcase-8b37bc3e.mp4',
  posterUrl: '/seedance-2-5-official-poster-db3ff793.jpg',
  sourceUrl: 'https://seed.bytedance.com/en/seedance2_5',
} as const

const chinaModelCards = [
  { name: 'Seedance', glyph: 'SD', color: 'bg-primary-600', descriptionKey: 'home.chinaExport.models.seedance' },
  { name: 'Seedream', glyph: 'SR', color: 'bg-blue-600', descriptionKey: 'home.chinaExport.models.seedream' },
  { name: 'Qwen', glyph: 'Q', color: 'bg-amber-600', descriptionKey: 'home.chinaExport.models.qwen' },
  { name: 'DeepSeek', glyph: 'D', color: 'bg-indigo-600', descriptionKey: 'home.chinaExport.models.deepseek' },
  { name: 'GLM', glyph: 'G', color: 'bg-cyan-700', descriptionKey: 'home.chinaExport.models.glm' },
  { name: 'Kimi', glyph: 'K', color: 'bg-violet-600', descriptionKey: 'home.chinaExport.models.kimi' },
] as const

const deepseekCurl = `curl https://api.tokenkey.dev/v1/chat/completions \\
  -H "Authorization: Bearer $TOKENKEY_API_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": "deepseek-chat",
    "messages": [
      {
        "role": "user",
        "content": "Say hello in one sentence."
      }
    ]
  }'`

const codeCopied = ref(false)
async function copyQuickstart() {
  await navigator.clipboard.writeText(deepseekCurl)
  codeCopied.value = true
  window.setTimeout(() => {
    codeCopied.value = false
  }, 1600)
}

const chinaFaqKeys = ['models', 'credit', 'data', 'payments'] as const
const openChinaFaqItems = reactive<Record<string, boolean>>({})
function toggleChinaFaq(key: string) {
  openChinaFaqItems[key] = !openChinaFaqItems[key]
}

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

// Use-case cards
const useCaseCards = [
  {
    key: 'aiCoding',
    icon: 'terminal',
    gradient: 'from-primary-500 to-primary-600 shadow-primary-500/30',
    title: 'home.useCases.aiCoding.title',
    desc: 'home.useCases.aiCoding.desc',
  },
  {
    key: 'creativeStudio',
    icon: 'sparkles',
    gradient: 'from-pink-500 to-rose-600 shadow-pink-500/30',
    title: 'home.useCases.creativeStudio.title',
    desc: 'home.useCases.creativeStudio.desc',
  },
  {
    key: 'teamSharing',
    icon: 'users',
    gradient: 'from-blue-500 to-indigo-600 shadow-blue-500/30',
    title: 'home.useCases.teamSharing.title',
    desc: 'home.useCases.teamSharing.desc',
  },
] as const

// FAQ accordion
const faqKeys = ['differ', 'models', 'billing', 'tools', 'trial', 'quotaUp'] as const
const openFaqItems = reactive<Record<string, boolean>>({})
function toggleFaq(key: string) {
  openFaqItems[key] = !openFaqItems[key]
}

const {
  currentYear,
  dashboardPath,
  docUrl,
  isAuthenticated,
  isDark,
  showModelPlazaEntry,
  siteLogo,
  toggleTheme,
  userInitial,
} = useHomeShell()
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
  display: grid;
  grid-template-columns: 52px minmax(0, 1fr) 52px;
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
  text-align: center;
  font-size: 12px;
  font-family: ui-monospace, monospace;
  color: #64748b;
}

.terminal-copy-button {
  display: flex;
  width: 32px;
  height: 32px;
  align-items: center;
  justify-content: center;
  justify-self: end;
  border-radius: 6px;
  color: #94a3b8;
  transition: color 0.15s ease, background-color 0.15s ease;
}

.terminal-copy-button:hover {
  color: #fff;
  background: rgba(255, 255, 255, 0.1);
}

/* Terminal Body */
.terminal-body {
  padding: 20px 24px;
  font-family: ui-monospace, 'Fira Code', monospace;
  font-size: 14px;
  line-height: 2;
}

.terminal-command-body {
  margin: 0;
  color: #cbd5e1;
  white-space: pre;
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
