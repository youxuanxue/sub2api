// One-shot client-side migration: when the account admin table changed its
// default-visible column set to include `last_used_at` + `expires_at`, users
// whose browsers had explicitly hidden those columns (via the column-settings
// UI before the change) would still see them hidden because the saved set
// took precedence over the new defaults. This helper resurfaces them once,
// guarded by a sentinel localStorage key so subsequent re-hides by the user
// are respected.

export const ACCOUNT_COLUMNS_TS_DEFAULT_VISIBLE_KEY = 'account-columns-ts-default-visible-v1'

/**
 * Mutates the hidden-column set in place to remove `last_used_at` and
 * `expires_at` if the migration sentinel has not yet been written; returns
 * true when the migration ran (so the caller can persist the set), false
 * when it was a no-op (sentinel already present, or storage threw).
 *
 * Storage is injected to keep the helper unit-testable without polluting
 * window.localStorage; production callers pass the real `localStorage`.
 */
export function migrateAccountTimestampColumnsVisibleOnce(
  hidden: Set<string>,
  storage: Pick<Storage, 'getItem' | 'setItem'> = localStorage,
): boolean {
  try {
    if (storage.getItem(ACCOUNT_COLUMNS_TS_DEFAULT_VISIBLE_KEY)) return false
    hidden.delete('last_used_at')
    hidden.delete('expires_at')
    storage.setItem(ACCOUNT_COLUMNS_TS_DEFAULT_VISIBLE_KEY, '1')
    return true
  } catch (e) {
    console.error('Failed to migrate account column visibility:', e)
    return false
  }
}
