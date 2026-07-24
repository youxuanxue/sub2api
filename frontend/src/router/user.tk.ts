/**
 * TK user route subtree — persistent UserShellView shell (mirrors admin.tk.ts / PR #935).
 *
 * WHY: Per-view <AppLayout> + App.vue KeepAlive cached whole pages (sidebar included),
 * so sidebar scroll reset on /keys and /studio. Hoist layout into UserShellView and
 * nest console pages as children so AppSidebar stays mounted.
 *
 * Guard: scripts/checks/user-shell-layout.py asserts user views render inside
 * UserShellView (no per-view <AppLayout>).
 */

import type { RouteRecordRaw } from 'vue-router'

const userShellChildren: RouteRecordRaw[] = [
  {
    path: 'dashboard',
    name: 'Dashboard',
    component: () => import('@/views/user/DashboardView.vue'),
    meta: {
      requiresAuth: true,
      requiresAdmin: false,
      title: 'Dashboard',
      titleKey: 'dashboard.title',
      descriptionKey: 'dashboard.welcomeMessage',
    },
  },
  {
    path: 'playground',
    redirect: { path: '/studio', query: { mode: 'chat' } },
  },
  {
    path: 'studio',
    name: 'Studio',
    component: () => import('@/views/user/studio/MediaStudioView.vue'),
    meta: {
      requiresAuth: true,
      requiresAdmin: false,
      title: 'Studio',
      titleKey: 'studio.title',
      descriptionKey: 'studio.subtitle',
    },
  },
  {
    path: 'quickstart',
    name: 'Quickstart',
    component: () => import('@/views/user/QuickstartView.vue'),
    meta: {
      requiresAuth: true,
      requiresAdmin: false,
      title: 'Tool Integrations',
      titleKey: 'quickstart.title',
      descriptionKey: 'quickstart.subtitle',
    },
  },
  {
    path: 'keys',
    name: 'Keys',
    component: () => import('@/views/user/KeysView.vue'),
    meta: {
      requiresAuth: true,
      requiresAdmin: false,
      title: 'API Keys',
      titleKey: 'keys.title',
      descriptionKey: 'keys.description',
    },
  },
  {
    path: 'batch-image',
    name: 'BatchImageGuide',
    alias: '/docs/batch-image',
    component: () => import('@/views/user/BatchImageGuideView.vue'),
    meta: {
      requiresAuth: true,
      requiresAdmin: false,
      title: 'Batch Image Guide',
      titleKey: 'batchImageGuide.title',
      descriptionKey: 'batchImageGuide.description',
    },
  },
  {
    path: 'usage',
    name: 'Usage',
    component: () => import('@/views/user/UsageView.vue'),
    meta: {
      requiresAuth: true,
      requiresAdmin: false,
      title: 'Usage Records',
      titleKey: 'usage.title',
      descriptionKey: 'usage.description',
    },
  },
  {
    path: 'redeem',
    name: 'Redeem',
    component: () => import('@/views/user/RedeemView.vue'),
    meta: {
      requiresAuth: true,
      requiresAdmin: false,
      title: 'Redeem Code',
      titleKey: 'redeem.title',
      descriptionKey: 'redeem.description',
    },
  },
  {
    path: 'affiliate',
    name: 'Affiliate',
    component: () => import('@/views/user/AffiliateView.vue'),
    meta: {
      requiresAuth: true,
      requiresAdmin: false,
      title: 'Affiliate',
      titleKey: 'affiliate.title',
      descriptionKey: 'affiliate.description',
    },
  },
  {
    path: 'available-channels',
    name: 'UserAvailableChannels',
    component: () => import('@/views/user/AvailableChannelsView.vue'),
    meta: {
      requiresAuth: true,
      requiresAdmin: false,
      title: 'Available Channels',
      titleKey: 'availableChannels.title',
      descriptionKey: 'availableChannels.description',
    },
  },
  {
    path: 'profile',
    name: 'Profile',
    component: () => import('@/views/user/ProfileView.vue'),
    meta: {
      requiresAuth: true,
      requiresAdmin: false,
      title: 'Profile',
      titleKey: 'profile.title',
      descriptionKey: 'profile.description',
    },
  },
  {
    path: 'subscriptions',
    name: 'Subscriptions',
    component: () => import('@/views/user/SubscriptionsView.vue'),
    meta: {
      requiresAuth: true,
      requiresAdmin: false,
      title: 'My Subscriptions',
      titleKey: 'userSubscriptions.title',
      descriptionKey: 'userSubscriptions.description',
    },
  },
  {
    path: 'purchase',
    name: 'PurchaseSubscription',
    component: () => import('@/views/user/PaymentView.vue'),
    meta: {
      requiresAuth: true,
      requiresAdmin: false,
      title: 'Purchase Subscription',
      titleKey: 'nav.buySubscription',
      descriptionKey: 'purchase.description',
      requiresPayment: true,
    },
  },
  {
    path: 'orders',
    name: 'OrderList',
    component: () => import('@/views/user/UserOrdersView.vue'),
    meta: {
      requiresAuth: true,
      requiresAdmin: false,
      title: 'My Orders',
      titleKey: 'nav.myOrders',
      requiresPayment: true,
    },
  },
  {
    path: 'payment/qrcode',
    name: 'PaymentQRCode',
    component: () => import('@/views/user/PaymentQRCodeView.vue'),
    meta: {
      requiresAuth: true,
      requiresAdmin: false,
      title: 'Payment',
      titleKey: 'payment.qr.scanToPay',
      requiresPayment: true,
    },
  },
  {
    path: 'custom/:id',
    name: 'CustomPage',
    component: () => import('@/views/user/CustomPageView.vue'),
    meta: {
      requiresAuth: true,
      requiresAdmin: false,
      title: 'Custom Page',
      titleKey: 'customPage.title',
    },
  },
  {
    path: 'monitor',
    name: 'ChannelStatus',
    component: () => import('@/views/user/ChannelStatusView.vue'),
    meta: {
      requiresAuth: true,
      requiresAdmin: false,
      title: 'Channel Status',
      titleKey: 'nav.channelStatus',
    },
  },
  {
    path: 'models',
    name: 'ModelMarketplace',
    component: () => import('@/views/CatalogHubView.vue'),
    meta: {
      requiresAuth: false,
      title: 'Model Marketplace',
      titleKey: 'models.title',
      descriptionKey: 'models.subtitle',
    },
  },
]

export const userRoutes: RouteRecordRaw[] = [
  {
    path: '/',
    component: () => import('@/views/user/UserShellView.vue'),
    children: userShellChildren,
  },
]
