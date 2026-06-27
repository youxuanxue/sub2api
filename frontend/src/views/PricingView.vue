<template>
  <div
    class="relative flex flex-col bg-gradient-to-br from-gray-50 via-primary-50/30 to-gray-100 dark:from-dark-950 dark:via-dark-900 dark:to-dark-950"
    :class="
      pricingCatalogScrollMode
        ? 'h-[100dvh] max-h-[100dvh] overflow-hidden'
        : 'min-h-screen'
    "
  >
    <main
      class="relative z-10 flex min-h-0 flex-1 flex-col px-4 pt-8 sm:px-6"
      :class="pricingCatalogScrollMode ? 'overflow-hidden pb-4' : 'pb-16'"
    >
      <!-- Sticky chrome: nav + hero (when catalog table is shown, only tbody scrolls) -->
      <div class="mx-auto w-full max-w-[90rem] shrink-0 pb-4">
        <header class="relative z-20 py-4 sm:py-0">
          <nav
            class="mx-auto flex max-w-[90rem] flex-wrap items-center justify-between gap-4"
            :aria-label="t('pricing.nav.aria')"
          >
            <div class="flex flex-wrap items-center gap-2">
              <router-link to="/home" :class="NAV_LINK_CLASS">
                <Icon name="home" size="sm" :class="NAV_ICON_CLASS" />
                <span>{{ t('pricing.nav.home') }}</span>
              </router-link>
              <router-link :to="consolePath" :class="NAV_LINK_CLASS" :title="consoleLinkTitle">
                <Icon name="grid" size="sm" :class="NAV_ICON_CLASS" />
                <span>{{ t('pricing.nav.console') }}</span>
              </router-link>
            </div>
            <LocaleSwitcher />
          </nav>
        </header>

        <div class="mt-6 text-center sm:mt-8">
          <h1
            class="text-3xl font-bold tracking-tight text-gray-900 dark:text-white sm:text-4xl"
          >
            {{ heroTitle }}
          </h1>
          <p class="mt-3 text-base text-gray-600 dark:text-dark-300">
            {{ heroSubtitle }}
          </p>
          <p class="mx-auto mt-4 max-w-3xl text-sm text-gray-500 dark:text-dark-400">
            {{ heroDescription }}
          </p>
          <div
            v-if="bonusCtaVisible"
            class="mx-auto mt-8 flex max-w-lg flex-col items-center gap-3 rounded-2xl border border-primary-200/70 bg-primary-50/90 px-6 py-5 text-center shadow-sm dark:border-primary-900/40 dark:bg-primary-950/40"
          >
            <router-link
              to="/register"
              class="inline-flex items-center justify-center rounded-xl bg-primary-600 px-6 py-3 text-sm font-semibold text-white shadow-sm transition hover:bg-primary-700 dark:bg-primary-500 dark:hover:bg-primary-600"
            >
              {{ t('pricing.ctaBonus', { amount: signupBonusFormatted }) }}
            </router-link>
            <p class="text-xs text-gray-600 dark:text-dark-400">{{ t('pricing.ctaBonusHint') }}</p>
          </div>

        </div>
      </div>

      <div class="mx-auto flex min-h-0 w-full max-w-[90rem] flex-1 flex-col">
        <!-- Unified catalog filters: key, group/public scope, search. -->
        <div
          v-if="!loading && !errorMessage"
          class="mb-4 flex flex-col gap-3"
        >
          <div
            class="rounded-2xl border border-gray-200 bg-white/80 px-4 py-3 shadow-sm dark:border-dark-800 dark:bg-dark-900/70"
          >
            <div
              class="grid gap-3"
              :class="canShowCatalogFilters ? 'lg:grid-cols-[minmax(14rem,1fr)_minmax(14rem,1fr)_minmax(20rem,2fr)]' : 'lg:grid-cols-[minmax(20rem,2fr)_auto]'"
            >
              <label
                v-if="canShowCatalogFilters"
                class="flex min-w-0 flex-col gap-1 text-sm text-gray-700 dark:text-dark-200"
              >
                <span class="text-xs font-medium uppercase text-gray-500 dark:text-dark-400">{{ t('pricing.filters.apiKey') }}</span>
                <select
                  :value="displayKeyId"
                  data-tk="pricing-filter-key"
                  class="w-full rounded-lg border border-gray-200 bg-white px-3 py-2 text-sm dark:border-dark-700 dark:bg-dark-900 dark:text-white"
                  @change="onPickKey"
                >
                  <option :value="0">{{ t('pricing.filters.keyPlaceholder') }}</option>
                  <option
                    v-for="k in myCatalog?.my_keys ?? []"
                    :key="k.id"
                    :value="k.id"
                  >
                    {{ k.name }} · {{ k.group_name }}
                  </option>
                </select>
              </label>

              <label
                v-if="canShowCatalogFilters"
                class="flex min-w-0 flex-col gap-1 text-sm text-gray-700 dark:text-dark-200"
              >
                <span class="text-xs font-medium uppercase text-gray-500 dark:text-dark-400">{{ t('pricing.filters.group') }}</span>
                <select
                  :value="displayGroupValue"
                  data-tk="pricing-filter-group"
                  class="w-full rounded-lg border border-gray-200 bg-white px-3 py-2 text-sm dark:border-dark-700 dark:bg-dark-900 dark:text-white"
                  @change="onPickGroup"
                >
                  <option value="public">{{ t('pricing.filters.publicCatalog') }}</option>
                  <option
                    v-for="g in groupFilterOptions"
                    :key="g.id"
                    :value="`group:${g.id}`"
                  >
                    {{ groupFilterOptionLabel(g) }}
                  </option>
                </select>
              </label>

              <label class="flex min-w-0 flex-col gap-1 text-sm text-gray-700 dark:text-dark-200">
                <span class="text-xs font-medium uppercase text-gray-500 dark:text-dark-400">{{ t('pricing.filters.search') }}</span>
                <span class="relative block min-w-0">
                  <Icon
                    name="search"
                    size="sm"
                    class="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-gray-400 dark:text-dark-500"
                  />
                  <input
                    id="pricing-model-search"
                    v-model="modelSearchQuery"
                    data-tk="pricing-filter-search"
                    type="search"
                    autocomplete="off"
                    :placeholder="t('pricing.search.placeholder')"
                    class="w-full rounded-lg border border-gray-200 bg-white py-2 pl-9 pr-3 text-sm text-gray-900 placeholder:text-gray-400 focus:border-primary-500 focus:outline-none focus:ring-2 focus:ring-primary-500/20 dark:border-dark-700 dark:bg-dark-900 dark:text-white dark:placeholder:text-dark-500"
                  />
                </span>
              </label>
            </div>

            <div class="mt-3 flex flex-col gap-3 border-t border-gray-100 pt-3 dark:border-dark-800 sm:flex-row sm:items-center sm:justify-between">
              <div role="tablist" class="flex w-fit rounded-lg border border-gray-200 bg-white p-0.5 text-xs font-medium dark:border-dark-700 dark:bg-dark-900">
                <button
                  v-for="opt in modalityOptions"
                  :key="opt.value"
                  role="tab"
                  type="button"
                  :aria-selected="pricingModality === opt.value"
                  class="rounded-md px-2.5 py-1 transition-colors"
                  :class="pricingModality === opt.value ? 'bg-primary-600 text-white' : 'text-gray-600 hover:text-primary-700 dark:text-dark-300'"
                  @click="pricingModality = opt.value"
                >
                  {{ opt.label }}
                </button>
              </div>
              <div class="flex flex-wrap items-center gap-4">
                <fieldset class="flex items-center gap-3 text-xs">
                  <legend class="sr-only">{{ t('pricing.search.modeLabel') }}</legend>
                  <label class="inline-flex cursor-pointer items-center gap-1.5 text-gray-700 dark:text-dark-200">
                    <input v-model="modelSearchMode" type="radio" value="fuzzy" class="text-primary-600" />
                    {{ t('pricing.search.modeFuzzy') }}
                  </label>
                  <label class="inline-flex cursor-pointer items-center gap-1.5 text-gray-700 dark:text-dark-200">
                    <input v-model="modelSearchMode" type="radio" value="exact" class="text-primary-600" />
                    {{ t('pricing.search.modeExact') }}
                  </label>
                </fieldset>
                <span
                  v-if="hasClientFilterActive"
                  class="text-xs tabular-nums text-gray-500 dark:text-dark-400"
                >
                  {{ t('pricing.search.resultCount', { count: filteredRows.length }) }}
                </span>
              </div>
            </div>
          </div>

          <!-- Banner: exploring a group the user doesn't have a key in -->
          <div
            v-if="exploreBanner"
            class="flex flex-col gap-2 rounded-2xl border border-primary-200 bg-primary-50/80 px-4 py-3 text-sm text-primary-900 shadow-sm dark:border-primary-900/50 dark:bg-primary-950/40 dark:text-primary-200 sm:flex-row sm:items-center sm:justify-between"
          >
            <p>{{ exploreBanner.message }}</p>
            <router-link
              :to="exploreBanner.ctaTo"
              class="inline-flex items-center justify-center rounded-lg bg-primary-600 px-3.5 py-1.5 text-xs font-semibold text-white shadow-sm transition hover:bg-primary-700 dark:bg-primary-500 dark:hover:bg-primary-600"
            >
              {{ exploreBanner.ctaLabel }}
            </router-link>
          </div>
        </div>

        <div
          v-if="loading"
          class="flex items-center justify-center rounded-2xl bg-white/80 py-24 shadow-sm backdrop-blur dark:bg-dark-900/60"
        >
          <Icon name="refresh" size="lg" class="animate-spin text-primary-500" />
          <span class="ml-3 text-sm text-gray-600 dark:text-dark-300">{{
            t('common.loading')
          }}</span>
        </div>

        <div
          v-else-if="errorMessage"
          class="rounded-2xl border border-red-200 bg-red-50/80 p-8 text-center dark:border-red-900/40 dark:bg-red-950/30"
        >
          <Icon name="exclamationTriangle" size="xl" class="mx-auto text-red-500" />
          <h2 class="mt-4 text-lg font-semibold text-red-800 dark:text-red-200">
            {{ t('pricing.errorTitle') }}
          </h2>
          <p class="mt-2 text-sm text-red-700/90 dark:text-red-300/80">{{ errorMessage }}</p>
          <p class="mt-1 text-xs text-red-600/80 dark:text-red-300/70">
            {{ t('pricing.errorHint') }}
          </p>
          <button
            type="button"
            class="mt-5 inline-flex items-center gap-1.5 rounded-lg border border-red-300 bg-white px-4 py-2 text-sm font-medium text-red-700 transition-colors hover:bg-red-100 dark:border-red-800 dark:bg-red-950/60 dark:text-red-200 dark:hover:bg-red-900/60"
            @click="reload"
          >
            <Icon name="refresh" size="sm" />
            {{ t('pricing.retry') }}
          </button>
        </div>

        <div
          v-else-if="normalizedRows.length === 0 && !hasFilterActive"
          class="rounded-2xl border border-dashed border-gray-300 bg-white/60 p-12 text-center dark:border-dark-700 dark:bg-dark-900/40"
        >
          <Icon name="inbox" size="xl" class="mx-auto text-gray-400 dark:text-dark-500" />
          <h2 class="mt-4 text-lg font-semibold text-gray-800 dark:text-dark-100">
            {{ emptyTitle }}
          </h2>
          <p class="mt-2 text-sm text-gray-500 dark:text-dark-400">
            {{ emptyHint }}
          </p>
        </div>

        <div
          v-else
          class="flex min-h-0 flex-1 flex-col overflow-hidden rounded-2xl border border-gray-200 bg-white shadow-sm dark:border-dark-800 dark:bg-dark-900"
          data-tk="cold-start-pricing-table"
        >
          <div
            class="flex shrink-0 flex-col gap-3 border-b border-gray-100 bg-gray-50/80 px-4 py-3 dark:border-dark-800 dark:bg-dark-800/40 sm:flex-row sm:items-center sm:justify-between"
          >
            <p class="text-xs text-gray-500 dark:text-dark-400" data-tk="pricing-active-catalog">{{ activeCatalogLabel }}</p>
            <button
              v-if="canExportPricing"
              type="button"
              :disabled="exporting"
              class="inline-flex items-center gap-1.5 self-start rounded-lg border border-gray-200 bg-white px-3 py-1.5 text-xs font-medium text-gray-700 shadow-sm transition hover:bg-gray-50 disabled:cursor-not-allowed disabled:opacity-60 dark:border-dark-700 dark:bg-dark-800 dark:text-dark-100 dark:hover:bg-dark-700 sm:self-auto"
              data-tk="pricing-export-csv"
              @click="onExportPricing"
            >
              <Icon name="download" size="sm" :stroke-width="2" />
              {{ t('pricing.export.button') }}
            </button>
          </div>
          <p
            class="shrink-0 border-b border-gray-100 bg-gray-50/50 px-4 py-2 text-xs text-gray-500 dark:border-dark-800 dark:bg-dark-800/30 dark:text-dark-400 lg:hidden"
          >
            {{ t('pricing.tableHint') }}
          </p>
          <div
            v-if="filteredRows.length === 0 && hasClientFilterActive"
            class="border-t border-gray-100 px-4 py-12 text-center dark:border-dark-800"
          >
            <Icon name="inbox" size="xl" class="mx-auto text-gray-400 dark:text-dark-500" />
            <p class="mt-3 text-sm font-medium text-gray-700 dark:text-dark-200">
              {{ t('pricing.search.noMatches') }}
            </p>
          </div>
          <div v-else class="flex min-h-0 flex-1 flex-col overflow-hidden">
            <div
              class="min-h-0 flex-1 overflow-x-auto overflow-y-auto overscroll-y-contain [-webkit-overflow-scrolling:touch]"
            >
              <table class="min-w-[72rem] w-full border-collapse divide-y divide-gray-200 dark:divide-dark-800">
                <thead class="bg-gray-50 dark:bg-dark-800/60">
                  <tr>
                    <th
                      scope="col"
                      class="sticky left-0 top-0 z-[45] min-w-[14rem] max-w-[28rem] border-r border-gray-200 bg-gray-50 px-3 py-3 text-left text-xs font-semibold uppercase tracking-wider text-gray-500 shadow-[4px_0_12px_-8px_rgba(0,0,0,0.15)] dark:border-dark-700 dark:bg-dark-800/60 dark:text-dark-300 dark:shadow-[4px_0_12px_-8px_rgba(0,0,0,0.4)]"
                    >
                      {{ t('pricing.columns.model') }}
                    </th>
                    <th
                      scope="col"
                      class="sticky top-0 z-40 min-w-[7rem] bg-gray-50 px-3 py-3 text-left text-xs font-semibold uppercase tracking-wider text-gray-500 dark:bg-dark-800/60 dark:text-dark-300"
                    >
                      {{ t('pricing.columns.vendor') }}
                    </th>
                    <th
                      scope="col"
                      class="sticky top-0 z-40 bg-gray-50 px-3 py-3 text-right text-xs font-semibold uppercase tracking-wider text-gray-500 dark:bg-dark-800/60 dark:text-dark-300"
                    >
                      {{ inputColumnTitle }}
                    </th>
                    <th
                      scope="col"
                      class="sticky top-0 z-40 bg-gray-50 px-3 py-3 text-right text-xs font-semibold uppercase tracking-wider text-gray-500 dark:bg-dark-800/60 dark:text-dark-300"
                    >
                      {{ outputColumnTitle }}
                    </th>
                    <th
                      v-if="hasCacheColumns"
                      scope="col"
                      class="sticky top-0 z-40 bg-gray-50 px-3 py-3 text-right text-xs font-semibold uppercase tracking-wider text-gray-500 dark:bg-dark-800/60 dark:text-dark-300"
                    >
                      {{ t('pricing.columns.cacheRead') }}
                    </th>
                    <th
                      v-if="hasCacheColumns"
                      scope="col"
                      class="sticky top-0 z-40 bg-gray-50 px-3 py-3 text-right text-xs font-semibold uppercase tracking-wider text-gray-500 dark:bg-dark-800/60 dark:text-dark-300"
                    >
                      {{ t('pricing.columns.cacheWrite') }}
                    </th>
                    <th
                      scope="col"
                      class="sticky top-0 z-40 bg-gray-50 px-3 py-3 text-right text-xs font-semibold uppercase tracking-wider text-gray-500 dark:bg-dark-800/60 dark:text-dark-300"
                    >
                      {{ t('pricing.columns.contextWindow') }}
                    </th>
                    <th
                      v-if="hasMaxOutputColumn"
                      scope="col"
                      class="sticky top-0 z-40 bg-gray-50 px-3 py-3 text-right text-xs font-semibold uppercase tracking-wider text-gray-500 dark:bg-dark-800/60 dark:text-dark-300"
                    >
                      {{ t('pricing.columns.maxOutput') }}
                    </th>
                    <th
                      scope="col"
                      class="sticky top-0 z-40 min-w-[10rem] bg-gray-50 px-3 py-3 text-left text-xs font-semibold uppercase tracking-wider text-gray-500 dark:bg-dark-800/60 dark:text-dark-300"
                    >
                      {{ t('pricing.columns.capabilities') }}
                    </th>
                    <th
                      v-if="showAuthorizedGroupsColumn"
                      scope="col"
                      data-tk="pricing-col-authorized-groups"
                      class="sticky top-0 z-40 min-w-[12rem] bg-gray-50 px-3 py-3 text-left text-xs font-semibold uppercase tracking-wider text-gray-500 dark:bg-dark-800/60 dark:text-dark-300"
                    >
                      {{ t('pricing.my.columns.authorizedGroups') }}
                    </th>
                  </tr>
                </thead>
                <tbody
                  class="divide-y divide-gray-100 bg-white dark:divide-dark-800/60 dark:bg-dark-900"
                >
                  <tr
                    v-for="row in filteredRows"
                    :key="row.model_id"
                    class="group hover:bg-primary-50/30 dark:hover:bg-dark-800/40"
                  >
                    <td
                      class="sticky left-0 z-10 min-w-[14rem] max-w-[28rem] border-r border-gray-200 bg-white px-3 py-3 align-top font-mono text-sm leading-snug text-gray-900 shadow-[4px_0_12px_-8px_rgba(0,0,0,0.12)] break-words group-hover:bg-primary-50/30 dark:border-dark-700 dark:bg-dark-900 dark:text-white dark:shadow-[4px_0_12px_-8px_rgba(0,0,0,0.45)] dark:group-hover:bg-dark-800/40"
                    >
                      {{ row.model_id }}
                      <span
                        v-if="row.billingMode && row.billingMode !== 'token'"
                        class="ml-1 inline-flex items-center rounded-md bg-amber-50 px-1.5 py-0.5 text-[10px] font-medium text-amber-800 dark:bg-amber-900/30 dark:text-amber-200"
                      >
                        {{ formatBillingMode(row.billingMode) }}
                      </span>
                    </td>
                    <td
                      class="min-w-[7rem] px-3 py-3 align-top text-sm leading-snug text-gray-600 break-words dark:text-dark-300"
                    >
                      {{ row.vendor || '—' }}
                    </td>
                    <td class="whitespace-nowrap px-3 py-3 text-right text-sm tabular-nums text-gray-900 dark:text-white">
                      <template v-if="row.billingMode === 'image' && row.perImage != null">
                        {{ formatPrice(row.perImage) }}
                        <span class="ml-0.5 text-xs text-gray-400">{{ t('pricing.perImage') }}</span>
                      </template>
                      <template v-else-if="row.billingMode === 'video' && row.perSecond != null">
                        {{ formatPrice(row.perSecond) }}
                        <span class="ml-0.5 text-xs text-gray-400">{{ t('pricing.perSecond') }}</span>
                      </template>
                      <template v-else-if="row.billingMode === 'per_request' && row.perRequest != null">
                        {{ formatPrice(row.perRequest) }}
                        <span class="ml-0.5 text-xs text-gray-400">{{ t('pricing.perRequest') }}</span>
                      </template>
                      <template v-else-if="row.inputPer1K != null">
                        {{ formatPrice(row.inputPer1K) }}
                        <span class="ml-0.5 text-xs text-gray-400">{{
                          t('pricing.perThousandTokens')
                        }}</span>
                        <div v-if="row.tiers && row.tiers.length" class="mt-0.5">
                          <span
                            class="inline-flex cursor-help items-center rounded bg-amber-50 px-1 py-px text-[10px] font-medium text-amber-700 dark:bg-amber-500/10 dark:text-amber-300"
                            :title="tierTooltip(row)"
                            data-tk="pricing-tier-badge"
                            >{{ t('pricing.tieredBadge', { n: row.tiers.length }) }}</span
                          >
                        </div>
                      </template>
                      <template v-else>—</template>
                    </td>
                    <td class="whitespace-nowrap px-3 py-3 text-right text-sm tabular-nums text-gray-900 dark:text-white">
                      <!-- Media (image/video) and per_request bill on a single unit; the output column is the worked example for video, "—" otherwise. -->
                      <template v-if="row.billingMode === 'video' && row.perSecond != null">
                        <span class="text-xs text-gray-500 dark:text-dark-400">{{ t('pricing.videoClipExample', { five: formatPrice(row.perSecond * 5), ten: formatPrice(row.perSecond * 10) }) }}</span>
                      </template>
                      <template v-else-if="row.billingMode === 'image' || row.billingMode === 'per_request'">—</template>
                      <template v-else-if="row.outputPer1K != null">
                        {{ formatPrice(row.outputPer1K) }}
                        <span class="ml-0.5 text-xs text-gray-400">{{
                          t('pricing.perThousandTokens')
                        }}</span>
                        <div v-if="row.thinkingOutputPer1K" class="mt-0.5 text-xs text-gray-500 dark:text-dark-400">
                          {{ t('pricing.thinkingOutput') }} {{ formatPrice(row.thinkingOutputPer1K) }}
                          <span class="text-gray-400">{{ t('pricing.perThousandTokens') }}</span>
                        </div>
                      </template>
                      <template v-else>—</template>
                    </td>
                    <td
                      v-if="hasCacheColumns"
                      class="whitespace-nowrap px-3 py-3 text-right text-sm tabular-nums text-gray-700 dark:text-dark-200"
                    >
                      {{ row.cacheReadPer1K != null ? formatPrice(row.cacheReadPer1K) : '—' }}
                    </td>
                    <td
                      v-if="hasCacheColumns"
                      class="whitespace-nowrap px-3 py-3 text-right text-sm tabular-nums text-gray-700 dark:text-dark-200"
                    >
                      {{ row.cacheWritePer1K != null ? formatPrice(row.cacheWritePer1K) : '—' }}
                    </td>
                    <td class="whitespace-nowrap px-3 py-3 text-right text-sm tabular-nums text-gray-600 dark:text-dark-300">
                      <template v-if="row.contextWindow && row.contextWindow > 0">
                        {{ formatNumber(row.contextWindow) }}
                      </template>
                      <template v-else>—</template>
                    </td>
                    <td
                      v-if="hasMaxOutputColumn"
                      class="whitespace-nowrap px-3 py-3 text-right text-sm tabular-nums text-gray-600 dark:text-dark-300"
                    >
                      <template v-if="row.maxOutputTokens && row.maxOutputTokens > 0">
                        {{ formatNumber(row.maxOutputTokens) }}
                      </template>
                      <template v-else>—</template>
                    </td>
                    <td class="px-3 py-3 align-top">
                      <div class="flex flex-wrap gap-1">
                        <span
                          v-for="cap in row.capabilities"
                          :key="cap"
                          class="inline-flex items-center rounded-full bg-primary-50 px-2 py-0.5 text-xs font-medium text-primary-700 dark:bg-primary-900/30 dark:text-primary-300"
                        >
                          {{ cap }}
                        </span>
                        <span
                          v-if="!row.capabilities || row.capabilities.length === 0"
                          class="text-xs text-gray-400"
                          >—</span
                        >
                      </div>
                    </td>
                    <td
                      v-if="showAuthorizedGroupsColumn"
                      data-tk="pricing-col-authorized-groups"
                      class="px-3 py-3 align-top"
                    >
                      <div class="flex flex-wrap gap-1.5">
                        <button
                          v-for="g in row.authorizedGroups || []"
                          :key="g.id"
                          type="button"
                          :title="t('pricing.my.authorizedGroups.createKeyHint', { group: g.name })"
                          class="inline-flex items-center gap-1 rounded-md border px-2 py-0.5 text-xs font-medium transition-colors"
                          :class="[
                            g.is_exclusive
                              ? 'border-primary-200 bg-primary-50 text-primary-700 hover:bg-primary-100 dark:border-primary-700/50 dark:bg-primary-900/30 dark:text-primary-200 dark:hover:bg-primary-900/50'
                              : 'border-gray-200 bg-gray-50 text-gray-600 hover:bg-gray-100 dark:border-dark-700 dark:bg-dark-800/60 dark:text-dark-300 dark:hover:bg-dark-700',
                            g.is_current_for_key ? 'ring-1 ring-primary-400/60' : ''
                          ]"
                          @click="onCreateKeyForGroup(g)"
                        >
                          <span>{{ g.name }}</span>
                          <span v-if="g.is_exclusive" class="text-[10px] opacity-70">{{
                            t('pricing.my.authorizedGroups.exclusive')
                          }}</span>
                        </button>
                        <span
                          v-if="!row.authorizedGroups || row.authorizedGroups.length === 0"
                          class="text-xs text-gray-400"
                          >—</span
                        >
                      </div>
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
            <div
              class="flex shrink-0 flex-col gap-2 border-t border-gray-100 bg-gray-50/60 px-4 py-3 text-xs text-gray-500 sm:flex-row sm:items-center sm:justify-between dark:border-dark-800 dark:bg-dark-800/40 dark:text-dark-400"
            >
              <p class="text-left tabular-nums">
                <template v-if="hasClientFilterActive">
                  {{
                    t('pricing.footer.filtered', {
                      shown: filteredRows.length,
                      total: rowTotal
                    })
                  }}
                </template>
                <template v-else>
                  {{ t('pricing.footer.total', { count: rowTotal }) }}
                </template>
              </p>
              <p class="text-left sm:text-right">
                {{ t('pricing.updatedAt', { time: formattedUpdatedAt }) }}
              </p>
            </div>
          </div>
        </div>
      </div>
    </main>
  </div>
</template>

<script setup lang="ts">
/**
 * Model + pricing catalog page.
 *
 * `/pricing` is one catalog surface with scope filters:
 *  - Public scope: GET /api/v1/public/pricing returns the platform-wide
 *    LiteLLM list-price catalog. Behaves per US-028 AC-005.
 *  - Authenticated group scope: GET /api/v1/me/pricing-catalog returns models
 *    scoped to the selected API key's group or an accessible group, at official
 *    list prices (decoupled from group/override rates).
 *
 * Both sources feed one normalized row shape so the table markup stays identical
 * while the UI avoids separate "group catalog" vs "public catalog" modes.
 */
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { getPublicPricing, type PublicCatalogResponse } from '@/api/pricing'
import {
  getMePricingCatalog,
  type MePricingCatalogResponse,
  type MePricingGroupRef,
  type MePricingModelGroup
} from '@/api/me-pricing'
import Icon from '@/components/icons/Icon.vue'
import LocaleSwitcher from '@/components/common/LocaleSwitcher.vue'
import { useAuthStore } from '@/stores/auth'
import { useAppStore } from '@/stores/app'
import { formatCurrency } from '@/utils/format'
import {
  filterPricingCatalogByModel,
  type PricingCatalogSearchMode
} from '@/utils/pricingCatalogSearch'
import { exportPricingCsv } from '@/composables/useTkPricingExport'

const { t } = useI18n()
const router = useRouter()

// 「授权分组」badge 点击：跳到 Keys 页并自动打开「创建密钥」面板、预置该分组。
// 用路由 name（'Keys'）而非硬编码路径，避免 path 漂移。
function onCreateKeyForGroup(g: MePricingModelGroup): void {
  void router.push({ name: 'Keys', query: { create: '1', group_id: String(g.id) } })
}
const authStore = useAuthStore()
const appStore = useAppStore()

type ViewMode = 'my' | 'public'

/**
 * Internal row shape — single source of truth for the shared table markup.
 *
 * Uses snake_case `model_id` (not camelCase modelId) so the row can flow
 * directly through `filterPricingCatalogByModel<T extends {model_id:string}>`
 * without an adapter mapping. Other fields stay camelCase because they're
 * derived/UI-shaped, not echoes of backend DTOs.
 */
interface NormalizedRow {
  model_id: string
  vendor: string
  inputPer1K: number | null
  outputPer1K: number | null
  thinkingOutputPer1K: number | null
  cacheReadPer1K: number | null
  cacheWritePer1K: number | null
  contextWindow: number
  maxOutputTokens: number
  capabilities: string[]
  billingMode?: string
  perRequest?: number | null
  perImage?: number | null
  perSecond?: number | null
  /** Input-token interval (阶梯) ladder, normalized from either catalog source. */
  tiers?: NormalizedTier[]
  /** Accessible groups that can serve this model — "授权分组" column when logged in. */
  authorizedGroups?: MePricingModelGroup[]
}

/** Normalized阶梯 bracket shared by public + my views (per-1k). */
interface NormalizedTier {
  minTokens: number
  maxTokens: number | null
  inputPer1K: number | null
  outputPer1K: number | null
}

const signupBonusFormatted = computed(() =>
  formatCurrency(appStore.cachedPublicSettings?.signup_bonus_balance_usd ?? 0, 'USD')
)

const bonusCtaVisible = computed(() => {
  const s = appStore.cachedPublicSettings
  if (!s?.registration_enabled) return false
  if (s.backend_mode_enabled) return false
  if (!s.signup_bonus_enabled) return false
  const amt = s.signup_bonus_balance_usd ?? 0
  return amt > 0 && !authStore.isAuthenticated
})

/** Shared nav pill styles — single source to reduce churn vs upstream-style pages. */
const NAV_LINK_CLASS =
  'group inline-flex items-center gap-2 rounded-xl border border-gray-200/80 bg-white/90 px-3.5 py-2 text-sm font-medium text-gray-700 shadow-sm backdrop-blur transition-colors hover:border-primary-200 hover:bg-primary-50/80 hover:text-primary-800 dark:border-dark-700 dark:bg-dark-900/70 dark:text-dark-200 dark:hover:border-primary-700/60 dark:hover:bg-primary-950/40 dark:hover:text-primary-200'
const NAV_ICON_CLASS =
  'text-gray-500 transition-colors group-hover:text-primary-600 dark:text-dark-400 dark:group-hover:text-primary-300'

const consolePath = computed(() => {
  if (!authStore.isAuthenticated) {
    return { path: '/login', query: { redirect: '/dashboard' } }
  }
  return authStore.isAdmin ? '/admin/dashboard' : '/dashboard'
})

const consoleLinkTitle = computed(() =>
  authStore.isAuthenticated ? t('pricing.nav.consoleTitleAuthed') : t('pricing.nav.consoleTitleGuest')
)

// ============================== view-state ==============================

const viewMode = ref<ViewMode>('public')
const loading = ref(true)
const errorMessage = ref('')
const modelSearchQuery = ref('')
const modelSearchMode = ref<PricingCatalogSearchMode>('fuzzy')

// Modality filter (text / image / video) — billing_mode-driven so media models
// (image per-image, video per-second) are discoverable instead of buried in a
// token-centric list.
type PricingModality = 'all' | 'text' | 'image' | 'video'
const pricingModality = ref<PricingModality>('all')
const modalityOptions = computed<{ value: PricingModality; label: string }[]>(() => [
  { value: 'all', label: t('pricing.modality.all') },
  { value: 'text', label: t('pricing.modality.text') },
  { value: 'image', label: t('pricing.modality.image') },
  { value: 'video', label: t('pricing.modality.video') }
])

function rowModality(billingMode?: string): PricingModality {
  if (billingMode === 'image') return 'image'
  if (billingMode === 'video') return 'video'
  return 'text'
}

function hasSavedAuthToken(): boolean {
  try {
    return typeof localStorage !== 'undefined' && !!localStorage.getItem('auth_token')
  } catch {
    return false
  }
}

// Public catalog state (unchanged from v1 — US-028 backing data).
const publicCatalog = ref<PublicCatalogResponse | null>(null)

// My catalog state.
const myCatalog = ref<MePricingCatalogResponse | null>(null)
const selectedKeyId = ref<number>(0)
const selectedGroupId = ref<number>(0)

const canShowCatalogFilters = computed(
  () =>
    myCatalog.value != null || authStore.isAuthenticated || hasSavedAuthToken()
)

/** Logged-in users see the authorized-groups column on both my and public views. */
const showAuthorizedGroupsColumn = computed(
  () => authStore.isAuthenticated || hasSavedAuthToken()
)

const authorizedGroupsByModel = computed<Record<string, MePricingModelGroup[]>>(() => {
  const fromIndex = myCatalog.value?.authorized_groups_by_model
  if (fromIndex && Object.keys(fromIndex).length > 0) {
    return fromIndex
  }
  const fromRows: Record<string, MePricingModelGroup[]> = {}
  for (const m of myCatalog.value?.models ?? []) {
    if (m.authorized_groups?.length) {
      fromRows[m.model_id] = m.authorized_groups
    }
  }
  return fromRows
})

// Admin-only "export platform pricing" — the public (在售目录/对外价) catalog only,
// as a sales-friendly CSV. Always visible for admins regardless of the current
// view; clicking switches to the public catalog first (and loads it if needed)
// so the export always reflects 对外价, never a per-key/group Your-Menu view.
const canExportPricing = computed(() => authStore.isAdmin)

const exporting = ref(false)

const onExportPricing = async (): Promise<void> => {
  if (exporting.value) return
  exporting.value = true
  try {
    // The CSV is always the public 对外价 catalog. If the admin is currently on
    // a Your-Menu (my) view, switch to public and (re)load it before exporting.
    if (viewMode.value !== 'public') {
      viewMode.value = 'public'
      selectedKeyId.value = 0
      selectedGroupId.value = 0
    }
    if ((publicCatalog.value?.data.length ?? 0) === 0) {
      await loadPublicCatalog()
    }
    if ((publicCatalog.value?.data.length ?? 0) === 0) {
      appStore.showError(t('pricing.export.empty'))
      return
    }
    exportPricingCsv(publicCatalog.value)
    appStore.showSuccess(t('pricing.export.success'))
  } catch {
    appStore.showError(t('pricing.export.empty'))
  } finally {
    exporting.value = false
  }
}

// ============================== hero copy ==============================

const heroTitle = computed(() => t('pricing.title'))
const heroSubtitle = computed(() => t('pricing.subtitle'))
const heroDescription = computed(() => t('pricing.description'))

const inputColumnTitle = computed(() =>
  viewMode.value === 'my' ? t('pricing.my.columns.input') : t('pricing.columns.input')
)
const outputColumnTitle = computed(() =>
  viewMode.value === 'my' ? t('pricing.my.columns.output') : t('pricing.columns.output')
)

// ============================== normalize ==============================

const normalizedRows = computed<NormalizedRow[]>(() => {
  if (viewMode.value === 'public') {
    if (!publicCatalog.value) return []
    return publicCatalog.value.data.map((m) => ({
      model_id: m.model_id,
      vendor: m.vendor ?? '',
      inputPer1K: m.pricing.input_per_1k_tokens ?? null,
      outputPer1K: m.pricing.output_per_1k_tokens ?? null,
      thinkingOutputPer1K: m.pricing.thinking_output_per_1k_tokens ?? null,
      cacheReadPer1K: m.pricing.cache_read_per_1k ?? null,
      cacheWritePer1K: m.pricing.cache_write_per_1k ?? null,
      contextWindow: m.context_window ?? 0,
      maxOutputTokens: m.max_output_tokens ?? 0,
      capabilities: m.capabilities ?? [],
      billingMode: m.pricing.billing_mode || 'token',
      perRequest: null,
      perImage: m.pricing.output_cost_per_image ?? null,
      perSecond: m.pricing.output_cost_per_second ?? null,
      tiers: m.pricing.tiers?.map((tt) => ({
        minTokens: tt.min_tokens,
        maxTokens: tt.max_tokens ?? null,
        inputPer1K: tt.input_per_1k_tokens ?? null,
        outputPer1K: tt.output_per_1k_tokens ?? null
      })),
      authorizedGroups: authorizedGroupsByModel.value[m.model_id] ?? []
    }))
  }
  // 'my' view
  if (!myCatalog.value) return []
  return myCatalog.value.models.map((m) => ({
    model_id: m.model_id,
    vendor: m.vendor ?? '',
    inputPer1K: m.your_price.input_per_1k ?? null,
    outputPer1K: m.your_price.output_per_1k ?? null,
    thinkingOutputPer1K: null,
    cacheReadPer1K: m.your_price.cache_read_per_1k ?? null,
    cacheWritePer1K: m.your_price.cache_write_per_1k ?? null,
    contextWindow: m.context_window ?? 0,
    maxOutputTokens: m.max_output_tokens ?? 0,
    capabilities: m.capabilities ?? [],
    billingMode: m.billing_mode,
    perRequest: m.your_price.per_request ?? null,
    perImage: m.your_price.per_image ?? null,
    perSecond: m.your_price.per_second ?? null,
    tiers: m.your_price.tiers?.map((tt) => ({
      minTokens: tt.min_tokens,
      maxTokens: tt.max_tokens ?? null,
      inputPer1K: tt.input_per_1k ?? null,
      outputPer1K: tt.output_per_1k ?? null
    })),
    authorizedGroups: m.authorized_groups ?? []
  }))
})

const filteredRows = computed(() => {
  const byName = filterPricingCatalogByModel(normalizedRows.value, modelSearchQuery.value, modelSearchMode.value)
  if (pricingModality.value === 'all') return byName
  return byName.filter((r) => rowModality(r.billingMode) === pricingModality.value)
})

const hasCacheColumns = computed(() =>
  normalizedRows.value.some(
    (r) => (r.cacheReadPer1K != null && r.cacheReadPer1K > 0) ||
           (r.cacheWritePer1K != null && r.cacheWritePer1K > 0)
  )
)

const hasMaxOutputColumn = computed(() =>
  normalizedRows.value.some((r) => r.maxOutputTokens > 0)
)

const rowTotal = computed(() => normalizedRows.value.length)
const hasClientFilterActive = computed(() =>
  modelSearchQuery.value.trim().length > 0 || pricingModality.value !== 'all'
)
const hasFilterActive = hasClientFilterActive

// ============================== empty-state copy ==============================

const emptyTitle = computed(() => {
  if (viewMode.value === 'my' && myCatalog.value) {
    return myCatalog.value.my_keys.length === 0 && myCatalog.value.accessible_groups.length === 0
      ? t('pricing.my.empty.noAccess.title')
      : t('pricing.my.empty.noModels.title')
  }
  return t('pricing.empty.title')
})

const emptyHint = computed(() => {
  if (viewMode.value === 'my' && myCatalog.value) {
    return myCatalog.value.my_keys.length === 0 && myCatalog.value.accessible_groups.length === 0
      ? t('pricing.my.empty.noAccess.hint')
      : t('pricing.my.empty.noModels.hint')
  }
  return t('pricing.empty.hint')
})

// ============================== updated-at ==============================

const formattedUpdatedAt = computed(() => {
  const raw =
    viewMode.value === 'my'
      ? myCatalog.value?.updated_at
      : publicCatalog.value?.updated_at
  if (!raw) return ''
  try {
    return new Date(raw).toLocaleString()
  } catch {
    return raw
  }
})

// ============================== group switcher ==============================
//
// TK: pricing 页（分组目录/所有分组）一律展示官方定价，与分组倍率/个人覆写
// 彻底脱钩——故不再有倍率提示（原 rateHint）、对比下拉不再拼 ×N 倍率角标。

const groupFilterOptions = computed<MePricingGroupRef[]>(() => {
  if (!myCatalog.value) return []
  const groups = [...myCatalog.value.accessible_groups]
  groups.sort((a, b) => {
    if (a.is_exclusive !== b.is_exclusive) return a.is_exclusive ? -1 : 1
    return a.name.localeCompare(b.name)
  })
  return groups
})

function groupFilterOptionLabel(g: MePricingGroupRef): string {
  if (g.is_exclusive) {
    return t('pricing.filters.groupExclusiveOption', { group: g.name })
  }
  return g.name
}

const displayGroupValue = computed(() => {
  if (viewMode.value === 'public') return 'public'
  const id = selectedGroupId.value > 0
    ? selectedGroupId.value
    : myCatalog.value?.target_group?.id
  return id && id > 0 ? `group:${id}` : 'public'
})

const activeCatalogLabel = computed(() => {
  if (viewMode.value === 'public') return t('pricing.filters.activePublic')
  const group = myCatalog.value?.target_group?.name
  if (!group) return t('pricing.my.title')
  const key = myCatalog.value?.my_keys.find((k) => k.id === displayKeyId.value)
  return key
    ? t('pricing.filters.activeKeyGroup', { key: key.name, group })
    : t('pricing.filters.activeGroup', { group })
})

interface ExploreBanner {
  message: string
  ctaLabel: string
  ctaTo: { path: string; query?: Record<string, string> }
}

const exploreBanner = computed<ExploreBanner | null>(() => {
  if (viewMode.value !== 'my' || !myCatalog.value) return null
  const tg = myCatalog.value.target_group
  if (!tg?.id) return null
  const currentKeysInTarget = myCatalog.value.my_keys.some((k) => k.group_id === tg.id)
  if (currentKeysInTarget) return null
  // User is viewing a group they don't hold a key in → upgrade-CTA banner.
  return {
    message: t('pricing.my.exploreBanner.message', {
      group: tg.name
    }),
    ctaLabel: t('pricing.my.exploreBanner.cta', { group: tg.name }),
    ctaTo: { path: '/dashboard/keys', query: { group_id: String(tg.id) } }
  }
})

// ============================== misc formatters ==============================

function formatPrice(value: number): string {
  if (!Number.isFinite(value)) return '—'
  if (value === 0) return '$0'
  if (value < 0.01) return `$${value.toFixed(6)}`
  if (value < 1) return `$${value.toFixed(4)}`
  return `$${value.toFixed(2)}`
}

function formatNumber(value: number): string {
  return new Intl.NumberFormat().format(value)
}

/** "0" / "32000"→"32k" / null→"∞" — compact token-bound label for the阶梯 tooltip. */
function tierTokenLabel(n: number | null): string {
  if (n === null) return '∞'
  if (n === 0) return '0'
  return n % 1000 === 0 ? `${n / 1000}k` : String(n)
}

/** Multi-line ladder for the row's tier badge `title` (per-1k, both views). */
function tierTooltip(row: NormalizedRow): string {
  if (!row.tiers || row.tiers.length === 0) return ''
  const unit = t('pricing.perThousandTokens')
  return row.tiers
    .map((tier) => {
      const range = `${tierTokenLabel(tier.minTokens)}–${tierTokenLabel(tier.maxTokens)}`
      const inp = tier.inputPer1K != null ? formatPrice(tier.inputPer1K) : '—'
      const out = tier.outputPer1K != null ? formatPrice(tier.outputPer1K) : '—'
      return `${range}: ${t('pricing.columns.input')} ${inp} / ${t('pricing.columns.output')} ${out} ${unit}`
    })
    .join('\n')
}

function formatBillingMode(mode: string): string {
  return t(`pricing.my.billingMode.${mode}`, mode)
}

// ============================== layout ==============================

const pricingCatalogScrollMode = computed(() => {
  if (loading.value || errorMessage.value) return false
  return rowTotal.value > 0
})

// ============================== loaders ==============================

async function loadPublicCatalog(): Promise<void> {
  try {
    publicCatalog.value = await getPublicPricing()
  } catch (err) {
    const apiErr = err as { status?: number; message?: string }
    if (apiErr.status === 404) {
      publicCatalog.value = { object: 'list', data: [], updated_at: '' }
    } else {
      throw err
    }
  }
}

async function loadMyCatalog(): Promise<void> {
  const params: { apiKeyId?: number; groupId?: number } = {}
  if (selectedGroupId.value > 0) {
    params.groupId = selectedGroupId.value
  } else if (selectedKeyId.value > 0) {
    params.apiKeyId = selectedKeyId.value
  }
  myCatalog.value = await getMePricingCatalog(params)
}

async function loadInitialCatalog(): Promise<void> {
  if (!hasSavedAuthToken()) {
    viewMode.value = 'public'
    await loadPublicCatalog()
    return
  }

  try {
    viewMode.value = 'my'
    await loadMyCatalog()
  } catch {
    viewMode.value = 'public'
    selectedKeyId.value = 0
    selectedGroupId.value = 0
    await loadPublicCatalog()
  }
}

/**
 * `displayKeyId` is the value the key-picker shows. Derived (never written
 * back into selectedKeyId by the loader) to avoid the watch-loop where a
 * post-load writeback fires another fetch. User picks are explicit via
 * onPickKey — no implicit reactive ping-pong.
 */
const displayKeyId = computed<number>(() => {
  if (viewMode.value === 'public' || selectedGroupId.value > 0) return 0
  if (selectedKeyId.value > 0) return selectedKeyId.value
  if (!myCatalog.value || myCatalog.value.my_keys.length === 0) return 0
  const tgID = myCatalog.value.target_group.id
  const match = myCatalog.value.my_keys.find((k) => k.group_id === tgID)
  return match?.id ?? myCatalog.value.my_keys[0].id
})

function onPickKey(e: Event): void {
  const next = Number((e.target as HTMLSelectElement).value)
  if (!Number.isFinite(next) || next < 0) return
  viewMode.value = 'my'
  selectedKeyId.value = next
  // Switching key clears any explore-group override.
  selectedGroupId.value = 0
  void load()
}

function onPickGroup(e: Event): void {
  const raw = (e.target as HTMLSelectElement).value
  if (raw === 'public') {
    viewMode.value = 'public'
    selectedKeyId.value = 0
    selectedGroupId.value = 0
    void load()
    return
  }
  const match = /^group:(\d+)$/.exec(raw)
  if (!match) return
  const next = Number(match[1])
  if (!Number.isFinite(next) || next <= 0) return
  viewMode.value = 'my'
  selectedKeyId.value = 0
  selectedGroupId.value = next
  void load()
}

async function ensureMyCatalogForAuthHints(): Promise<void> {
  if (!hasSavedAuthToken() && !authStore.isAuthenticated) return
  if (myCatalog.value?.authorized_groups_by_model) return
  try {
    myCatalog.value = await getMePricingCatalog()
  } catch {
    // Public catalog still works without auth hints.
  }
}

async function load(): Promise<void> {
  loading.value = true
  errorMessage.value = ''
  try {
    if (!myCatalog.value && selectedKeyId.value === 0 && selectedGroupId.value === 0) {
      await loadInitialCatalog()
    } else if (viewMode.value === 'my') {
      await loadMyCatalog()
    } else {
      await Promise.all([ensureMyCatalogForAuthHints(), loadPublicCatalog()])
    }
  } catch (err) {
    const apiErr = err as { message?: string }
    errorMessage.value = apiErr.message || 'Network error'
  } finally {
    loading.value = false
  }
}

function reload(): void {
  void load()
}

onMounted(() => {
  void load()
  void appStore.fetchPublicSettings()
})
</script>
