/**
 * TK admin route subtree — isolated from the upstream-shaped router/index.ts.
 *
 * WHY: PR #935 nested every admin page under a persistent AdminShellView shell
 * (children:[]), rewriting the admin block of router/index.ts and diverging it
 * heavily from upstream/main. Keeping that nested tree here lets index.ts carry a
 * thin `...adminRoutes` spread instead of ~240 conflict-prone lines, per
 * CLAUDE.md §5 ("large upstream files should read almost unchanged except for thin
 * injection points"). Add new admin pages HERE, not in index.ts.
 *
 * Guard: scripts/checks/admin-shell-layout.py asserts these views render inside
 * AdminShellView (no per-view <AppLayout>) so an upstream merge cannot silently
 * regress the persistent-shell layout.
 */

import type { RouteRecordRaw } from 'vue-router'

export const adminRoutes: RouteRecordRaw[] = [
  {
    // TK: edge admin-session handoff — chrome-less, establishes session on edge origin.
    path: '/admin/edge-handoff',
    name: 'AdminEdgeHandoff',
    component: () => import('@/views/admin/EdgeHandoffView.vue'),
    meta: {
      requiresAuth: false,
      title: 'Signing in…'
    }
  },
  {
    path: '/admin',
    component: () => import('@/views/admin/AdminShellView.vue'),
    meta: {
      requiresAuth: true,
      requiresAdmin: true
    },
    children: [
      { path: '', redirect: { name: 'AdminDashboard' } },
      {
        path: 'dashboard',
        name: 'AdminDashboard',
        component: () => import('@/views/admin/DashboardView.vue'),
        meta: {
          title: 'Admin Dashboard',
          titleKey: 'admin.dashboard.title',
          descriptionKey: 'admin.dashboard.description'
        }
      },
      {
        path: 'ops',
        name: 'AdminOps',
        component: () => import('@/views/admin/ops/OpsDashboard.vue'),
        meta: {
          title: 'Ops Monitoring',
          titleKey: 'admin.ops.title',
          descriptionKey: 'admin.ops.description'
        }
      },
      {
        path: 'users',
        name: 'AdminUsers',
        component: () => import('@/views/admin/UsersView.vue'),
        meta: {
          title: 'User Management',
          titleKey: 'admin.users.title',
          descriptionKey: 'admin.users.description'
        }
      },
      {
        path: 'groups',
        name: 'AdminGroups',
        component: () => import('@/views/admin/GroupsView.vue'),
        meta: {
          title: 'Group Management',
          titleKey: 'admin.groups.title',
          descriptionKey: 'admin.groups.description'
        }
      },
      { path: 'channels', redirect: { name: 'AdminChannels' } },
      {
        path: 'channels/pricing',
        name: 'AdminChannels',
        component: () => import('@/views/admin/ChannelsView.vue'),
        meta: {
          title: 'Channel Management',
          titleKey: 'admin.channels.title',
          descriptionKey: 'admin.channels.description'
        }
      },
      {
        path: 'channels/monitor',
        name: 'AdminChannelMonitor',
        component: () => import('@/views/admin/ChannelMonitorView.vue'),
        meta: {
          title: 'Channel Monitor',
          titleKey: 'admin.channelMonitor.title',
          descriptionKey: 'admin.channelMonitor.description'
        }
      },
      {
        path: 'subscriptions',
        name: 'AdminSubscriptions',
        component: () => import('@/views/admin/SubscriptionsView.vue'),
        meta: {
          title: 'Subscription Management',
          titleKey: 'admin.subscriptions.title',
          descriptionKey: 'admin.subscriptions.description'
        }
      },
      {
        path: 'accounts',
        name: 'AdminAccounts',
        component: () => import('@/views/admin/AccountsView.vue'),
        meta: {
          title: 'Account Management',
          titleKey: 'admin.accounts.title',
          descriptionKey: 'admin.accounts.description'
        }
      },
      {
        path: 'edge-accounts',
        name: 'AdminEdgeAccounts',
        component: () => import('@/views/admin/EdgeAccountsView.vue'),
        meta: {
          title: 'Edge Accounts',
          titleKey: 'admin.edgeAccounts.title',
          descriptionKey: 'admin.edgeAccounts.description'
        }
      },
      {
        path: 'announcements',
        name: 'AdminAnnouncements',
        component: () => import('@/views/admin/AnnouncementsView.vue'),
        meta: {
          title: 'Announcements',
          titleKey: 'admin.announcements.title',
          descriptionKey: 'admin.announcements.description'
        }
      },
      {
        path: 'proxies',
        name: 'AdminProxies',
        component: () => import('@/views/admin/ProxiesView.vue'),
        meta: {
          title: 'Proxy Management',
          titleKey: 'admin.proxies.title',
          descriptionKey: 'admin.proxies.description'
        }
      },
      {
        path: 'redeem',
        name: 'AdminRedeem',
        component: () => import('@/views/admin/RedeemView.vue'),
        meta: {
          title: 'Redeem Code Management',
          titleKey: 'admin.redeem.title',
          descriptionKey: 'admin.redeem.description'
        }
      },
      {
        path: 'promo-codes',
        name: 'AdminPromoCodes',
        component: () => import('@/views/admin/PromoCodesView.vue'),
        meta: {
          title: 'Promo Code Management',
          titleKey: 'admin.promo.title',
          descriptionKey: 'admin.promo.description'
        }
      },
      {
        path: 'settings',
        name: 'AdminSettings',
        component: () => import('@/views/admin/SettingsView.vue'),
        meta: {
          title: 'System Settings',
          titleKey: 'admin.settings.title',
          descriptionKey: 'admin.settings.description'
        }
      },
      {
        path: 'risk-control',
        name: 'AdminRiskControl',
        component: () => import('@/views/admin/RiskControlView.vue'),
        meta: {
          title: 'Risk Control',
          titleKey: 'admin.riskControl.title',
          descriptionKey: 'admin.riskControl.description',
          requiresRiskControl: true
        }
      },
      {
        path: 'usage',
        name: 'AdminUsage',
        component: () => import('@/views/admin/UsageView.vue'),
        meta: {
          title: 'Usage Records',
          titleKey: 'admin.usage.title',
          descriptionKey: 'admin.usage.description'
        }
      },
      { path: 'affiliates', redirect: { name: 'AdminAffiliateInvites' } },
      {
        path: 'affiliates/invites',
        name: 'AdminAffiliateInvites',
        component: () => import('@/views/admin/affiliates/AdminAffiliateInvitesView.vue'),
        meta: {
          title: 'Affiliate Invite Records',
          titleKey: 'nav.affiliateInviteRecords',
          descriptionKey: 'admin.affiliates.invitesDescription'
        }
      },
      {
        path: 'affiliates/rebates',
        name: 'AdminAffiliateRebates',
        component: () => import('@/views/admin/affiliates/AdminAffiliateRebatesView.vue'),
        meta: {
          title: 'Affiliate Rebate Records',
          titleKey: 'nav.affiliateRebateRecords',
          descriptionKey: 'admin.affiliates.rebatesDescription'
        }
      },
      {
        path: 'affiliates/transfers',
        name: 'AdminAffiliateTransfers',
        component: () => import('@/views/admin/affiliates/AdminAffiliateTransfersView.vue'),
        meta: {
          title: 'Affiliate Transfer Records',
          titleKey: 'nav.affiliateTransferRecords',
          descriptionKey: 'admin.affiliates.transfersDescription'
        }
      },
      {
        path: 'orders/dashboard',
        name: 'AdminPaymentDashboard',
        component: () => import('@/views/admin/orders/AdminPaymentDashboardView.vue'),
        meta: {
          title: 'Payment Dashboard',
          titleKey: 'nav.paymentDashboard',
          requiresPayment: true
        }
      },
      {
        path: 'orders',
        name: 'AdminOrders',
        component: () => import('@/views/admin/orders/AdminOrdersView.vue'),
        meta: {
          title: 'Order Management',
          titleKey: 'nav.orderManagement',
          requiresPayment: true
        }
      },
      {
        path: 'orders/plans',
        name: 'AdminPaymentPlans',
        component: () => import('@/views/admin/orders/AdminPaymentPlansView.vue'),
        meta: {
          title: 'Subscription Plans',
          titleKey: 'nav.paymentPlans',
          requiresPayment: true
        }
      }
    ]
  },
]
