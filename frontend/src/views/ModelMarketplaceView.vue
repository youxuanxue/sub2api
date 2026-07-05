<template>
  <div
    class="relative flex min-h-screen flex-col bg-gradient-to-br from-gray-50 via-primary-50/30 to-gray-100 dark:from-dark-950 dark:via-dark-900 dark:to-dark-950"
  >
    <main class="relative z-10 flex-1 px-4 pb-16 pt-6 sm:px-6">
      <div class="mx-auto max-w-6xl">
        <!-- Header -->
        <div class="mb-8 text-center">
          <h1 class="mb-2 text-3xl font-bold text-gray-900 dark:text-white sm:text-4xl">
            {{ t('models.title') }}
          </h1>
          <p class="text-base text-gray-600 dark:text-dark-400">
            {{ t('models.subtitle') }}
          </p>
          <!-- Back nav -->
          <div class="mt-4 flex items-center justify-center gap-3">
            <router-link
              to="/home"
              class="inline-flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-sm text-gray-500 transition-colors hover:bg-gray-100 hover:text-gray-700 dark:text-dark-400 dark:hover:bg-dark-800 dark:hover:text-white"
            >
              <svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="1.5">
                <path stroke-linecap="round" stroke-linejoin="round" d="M10.5 19.5L3 12m0 0l7.5-7.5M3 12h18" />
              </svg>
              {{ t('pricing.nav.home') }}
            </router-link>
            <router-link
              to="/pricing"
              class="inline-flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-sm text-gray-500 transition-colors hover:bg-gray-100 hover:text-gray-700 dark:text-dark-400 dark:hover:bg-dark-800 dark:hover:text-white"
            >
              {{ t('models.viewPricing') }}
            </router-link>
          </div>
        </div>

        <!-- Search + Filter Bar -->
        <div class="mb-6 flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
          <!-- Search -->
          <div class="relative flex-1 sm:max-w-sm">
            <svg
              class="absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-gray-400"
              fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2"
            >
              <path stroke-linecap="round" stroke-linejoin="round" d="M21 21l-5.197-5.197m0 0A7.5 7.5 0 105.196 5.196a7.5 7.5 0 0010.607 10.607z" />
            </svg>
            <input
              v-model="searchQuery"
              type="text"
              :placeholder="t('models.searchPlaceholder')"
              class="w-full rounded-lg border border-gray-200 bg-white py-2 pl-10 pr-4 text-sm text-gray-900 placeholder-gray-400 transition-colors focus:border-primary-500 focus:outline-none focus:ring-1 focus:ring-primary-500 dark:border-dark-700 dark:bg-dark-800 dark:text-white dark:placeholder-dark-500"
            />
          </div>

          <!-- Category Filter Pills -->
          <div class="flex flex-wrap gap-2">
            <button
              v-for="filter in categoryFilters"
              :key="filter.key"
              @click="activeCategory = filter.key"
              class="rounded-full px-4 py-1.5 text-sm font-medium transition-all"
              :class="
                activeCategory === filter.key
                  ? 'bg-primary-500 text-white shadow-sm'
                  : 'bg-white text-gray-600 hover:bg-gray-100 dark:bg-dark-800 dark:text-dark-300 dark:hover:bg-dark-700'
              "
            >
              {{ filter.label }}
            </button>
          </div>
        </div>

        <!-- Content: Sidebar + Grid -->
        <div class="flex flex-col gap-6 lg:flex-row">
          <!-- Provider Sidebar (desktop) -->
          <aside class="hidden w-56 shrink-0 lg:block">
            <div class="sticky top-6 rounded-xl border border-gray-200/60 bg-white/80 p-4 backdrop-blur-sm dark:border-dark-700/60 dark:bg-dark-800/80">
              <h3 class="mb-3 text-xs font-semibold uppercase tracking-wider text-gray-500 dark:text-dark-400">
                {{ t('models.providers') }}
              </h3>
              <ul class="space-y-1">
                <li>
                  <button
                    @click="activeVendor = null"
                    class="w-full rounded-lg px-3 py-1.5 text-left text-sm transition-colors"
                    :class="
                      activeVendor === null
                        ? 'bg-primary-50 font-medium text-primary-700 dark:bg-primary-900/30 dark:text-primary-300'
                        : 'text-gray-600 hover:bg-gray-50 dark:text-dark-300 dark:hover:bg-dark-700'
                    "
                  >
                    {{ t('models.filterAll') }}
                    <span class="ml-1 text-xs text-gray-400 dark:text-dark-500">({{ filteredByCategory.length }})</span>
                  </button>
                </li>
                <li v-for="vendor in vendorList" :key="vendor.name">
                  <button
                    @click="activeVendor = vendor.name"
                    class="w-full rounded-lg px-3 py-1.5 text-left text-sm transition-colors"
                    :class="
                      activeVendor === vendor.name
                        ? 'bg-primary-50 font-medium text-primary-700 dark:bg-primary-900/30 dark:text-primary-300'
                        : 'text-gray-600 hover:bg-gray-50 dark:text-dark-300 dark:hover:bg-dark-700'
                    "
                  >
                    {{ vendor.name }}
                    <span class="ml-1 text-xs text-gray-400 dark:text-dark-500">({{ vendor.count }})</span>
                  </button>
                </li>
              </ul>
            </div>
          </aside>

          <!-- Mobile Provider Filter -->
          <div class="flex flex-wrap gap-2 lg:hidden">
            <button
              @click="activeVendor = null"
              class="rounded-full px-3 py-1 text-xs font-medium transition-all"
              :class="
                activeVendor === null
                  ? 'bg-gray-900 text-white dark:bg-white dark:text-gray-900'
                  : 'bg-gray-100 text-gray-600 dark:bg-dark-800 dark:text-dark-300'
              "
            >
              {{ t('models.filterAll') }}
            </button>
            <button
              v-for="vendor in vendorList"
              :key="'m-' + vendor.name"
              @click="activeVendor = vendor.name"
              class="rounded-full px-3 py-1 text-xs font-medium transition-all"
              :class="
                activeVendor === vendor.name
                  ? 'bg-gray-900 text-white dark:bg-white dark:text-gray-900'
                  : 'bg-gray-100 text-gray-600 dark:bg-dark-800 dark:text-dark-300'
              "
            >
              {{ vendor.name }} ({{ vendor.count }})
            </button>
          </div>

          <!-- Model Cards Grid -->
          <div class="min-w-0 flex-1">
            <!-- Loading -->
            <div v-if="loading" class="flex items-center justify-center py-20">
              <div class="h-8 w-8 animate-spin rounded-full border-2 border-primary-500 border-t-transparent"></div>
            </div>

            <!-- Empty -->
            <div v-else-if="filteredModels.length === 0" class="py-20 text-center">
              <p class="text-gray-500 dark:text-dark-400">{{ t('models.noModels') }}</p>
            </div>

            <!-- Grid -->
            <div v-else class="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
              <router-link
                v-for="model in filteredModels"
                :key="model.model_id"
                :to="{ path: '/pricing', query: { model: model.model_id } }"
                class="group rounded-xl border border-gray-200/60 bg-white p-5 transition-all duration-200 hover:-translate-y-0.5 hover:border-primary-300 hover:shadow-lg hover:shadow-primary-500/10 dark:border-dark-700/60 dark:bg-dark-800 dark:hover:border-primary-700"
              >
                <!-- Model Name + Vendor -->
                <div class="mb-3 flex items-start justify-between gap-2">
                  <h3 class="min-w-0 break-all text-sm font-semibold text-gray-900 group-hover:text-primary-600 dark:text-white dark:group-hover:text-primary-400">
                    {{ model.model_id }}
                  </h3>
                  <span
                    v-if="model.vendor"
                    class="shrink-0 rounded-full bg-gray-100 px-2 py-0.5 text-[10px] font-medium text-gray-600 dark:bg-dark-700 dark:text-dark-300"
                  >
                    {{ model.vendor }}
                  </span>
                </div>

                <!-- Capabilities -->
                <div class="mb-3 flex flex-wrap gap-1.5">
                  <span
                    v-for="cap in model.capabilities"
                    :key="cap"
                    class="rounded-md px-2 py-0.5 text-[10px] font-medium"
                    :class="capabilityClass(cap)"
                  >
                    {{ formatCapabilityLabel(cap) }}
                  </span>
                </div>

                <!-- Pricing -->
                <div v-if="model.pricing" class="flex items-center gap-4 border-t border-gray-100 pt-3 dark:border-dark-700">
                  <div class="flex-1">
                    <span class="text-[10px] uppercase tracking-wider text-gray-400 dark:text-dark-500">{{ t('models.inputPrice') }}</span>
                    <p class="text-sm font-semibold text-gray-900 dark:text-white">
                      {{ formatPrice(model.pricing.input_per_1k_tokens) }}
                    </p>
                  </div>
                  <div class="flex-1">
                    <span class="text-[10px] uppercase tracking-wider text-gray-400 dark:text-dark-500">{{ t('models.outputPrice') }}</span>
                    <p class="text-sm font-semibold text-gray-900 dark:text-white">
                      {{ formatPrice(model.pricing.output_per_1k_tokens) }}
                    </p>
                  </div>
                  <span class="text-[10px] text-gray-400 dark:text-dark-500">{{ t('models.pricePerK') }}</span>
                </div>
              </router-link>
            </div>
          </div>
        </div>

        <!-- Bottom CTA -->
        <div class="mt-12 text-center">
          <router-link
            :to="isAuthenticated ? '/quickstart' : '/register'"
            class="inline-flex items-center gap-2 rounded-full bg-primary-500 px-6 py-2.5 text-sm font-medium text-white shadow-lg shadow-primary-500/30 transition-all hover:bg-primary-600"
          >
            {{ isAuthenticated ? t('nav.quickstart') : t('auth.createAccount') }}
            <svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2">
              <path stroke-linecap="round" stroke-linejoin="round" d="M13.5 4.5L21 12m0 0l-7.5 7.5M21 12H3" />
            </svg>
          </router-link>
        </div>
      </div>
    </main>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import { getPublicPricing, type PublicCatalogModel } from '@/api/pricing'

const { t, te } = useI18n()
const authStore = useAuthStore()

const isAuthenticated = computed(() => authStore.isAuthenticated)

const loading = ref(true)
const models = ref<PublicCatalogModel[]>([])
const searchQuery = ref('')
const activeCategory = ref<string>('all')
const activeVendor = ref<string | null>(null)

const categoryFilters = computed(() => [
  { key: 'all', label: t('models.filterAll') },
  { key: 'text', label: t('models.filterText') },
  { key: 'image', label: t('models.filterImage') },
  { key: 'video', label: t('models.filterVideo') },
])

// Filter by category (capability-based)
const filteredByCategory = computed(() => {
  if (activeCategory.value === 'all') return models.value
  if (activeCategory.value === 'image') {
    return models.value.filter(m => m.capabilities.includes('image_generation'))
  }
  if (activeCategory.value === 'video') {
    return models.value.filter(m => m.capabilities.includes('video_generation'))
  }
  // text: exclude image/video-only models
  return models.value.filter(m =>
    !m.capabilities.includes('image_generation') && !m.capabilities.includes('video_generation')
  )
})

// Vendor list derived from category-filtered models
const vendorList = computed(() => {
  const map = new Map<string, number>()
  for (const m of filteredByCategory.value) {
    const v = m.vendor || 'Unknown'
    map.set(v, (map.get(v) || 0) + 1)
  }
  return Array.from(map.entries())
    .map(([name, count]) => ({ name, count }))
    .sort((a, b) => b.count - a.count)
})

// Final filtered list
const filteredModels = computed(() => {
  let result = filteredByCategory.value

  // Vendor filter
  if (activeVendor.value) {
    result = result.filter(m => (m.vendor || 'Unknown') === activeVendor.value)
  }

  // Search filter
  if (searchQuery.value.trim()) {
    const q = searchQuery.value.toLowerCase().trim()
    result = result.filter(m => m.model_id.toLowerCase().includes(q))
  }

  return result
})

function formatPrice(price: number): string {
  if (price === 0) return 'Free'
  if (price < 0.001) return `$${price.toFixed(6)}`
  if (price < 0.01) return `$${price.toFixed(4)}`
  return `$${price.toFixed(3)}`
}

function formatCapabilityLabel(cap: string): string {
  const key = `models.capabilities.${cap}`
  if (te(key)) return t(key)
  return cap.replace(/_/g, ' ').replace(/\b\w/g, (c) => c.toUpperCase())
}

function capabilityClass(cap: string): string {
  switch (cap) {
    case 'vision':
      return 'bg-blue-50 text-blue-700 dark:bg-blue-900/30 dark:text-blue-300'
    case 'image_generation':
      return 'bg-purple-50 text-purple-700 dark:bg-purple-900/30 dark:text-purple-300'
    case 'video_generation':
      return 'bg-pink-50 text-pink-700 dark:bg-pink-900/30 dark:text-pink-300'
    case 'tools':
    case 'function_calling':
      return 'bg-amber-50 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300'
    case 'thinking':
      return 'bg-teal-50 text-teal-700 dark:bg-teal-900/30 dark:text-teal-300'
    default:
      return 'bg-gray-50 text-gray-600 dark:bg-dark-700 dark:text-dark-300'
  }
}

onMounted(async () => {
  try {
    const response = await getPublicPricing()
    models.value = response.data
  } catch (e) {
    console.error('Failed to load model catalog:', e)
  } finally {
    loading.value = false
  }
})
</script>
