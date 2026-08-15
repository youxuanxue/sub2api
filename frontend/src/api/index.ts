/**
 * API Client for Sub2API Backend
 * Central export point for all API modules
 */

// Re-export the HTTP client
export { apiClient } from './client'
export { isBrowserOffline, isNetworkError } from './client.tk'
export type { ApiNetworkError } from './client.tk'

// Auth API
export { authAPI, isTotp2FARequired, type LoginResponse } from './auth'

// User APIs
export { keysAPI } from './keys'
export { qaBundleAPI, type QABundleJob, type QABundleExportJob, type QABundleRecord } from './qaBundle'
export { usageAPI } from './usage'
export { userAPI } from './user'
export { redeemAPI, type RedeemHistoryItem } from './redeem'
export { paymentAPI } from './payment'
export { userGroupsAPI } from './groups'
export { userChannelsAPI } from './channels'
export * as batchImageAPI from './batchImage'
export { totpAPI } from './totp'
export { passkeyAPI, type PasskeyCredentialSummary } from './passkey'
export { default as announcementsAPI } from './announcements'
export { channelMonitorUserAPI } from './channelMonitor'

// Admin APIs
export { adminAPI } from './admin'

// Default export
export { default } from './client'
