// TokenKey-only layout tokens.
//
// Per CLAUDE.md §5 "Upstream Isolation → Vue/admin UI guidance": TK-specific
// styling that diverges from upstream defaults should live in this folder so
// that upstream-shaped `.vue` files (AppLayout / AppSidebar / etc.) remain
// thin template + import + reference. Touching these constants is the single
// place to retune sidebar geometry without sprinkling Tailwind class strings
// across multiple upstream-shaped sites and inviting merge conflicts at every
// upstream pull.
//
// The collapsed-rail width (`w-[72px]`) is deliberately NOT extracted: it is
// the upstream-shaped value and shared across forks; we only diverge on the
// expanded width.

/** Tailwind class for the expanded sidebar's width. Upstream is `w-64`; TK uses `w-44`. */
export const TK_SIDEBAR_WIDTH_CLASS = 'w-44'

/**
 * Tailwind class for the main content area's left margin when the sidebar is
 * expanded. Must move in lockstep with {@link TK_SIDEBAR_WIDTH_CLASS} — the
 * two are coupled by construction (margin == sidebar width). Tailwind's JIT
 * cannot resolve a runtime-built class string, so this is duplicated as a
 * literal rather than computed from the width token.
 */
export const TK_SIDEBAR_MAIN_MARGIN_CLASS = 'lg:ml-44'
