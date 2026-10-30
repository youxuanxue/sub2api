export interface FloatingPanelPosition {
  top: number | null
  bottom: number | null
  left: number
  width: number
  maxHeight: number
}

/**
 * Coordinates returned by getBoundingClientRect() are visual viewport pixels.
 * The admin shell applies CSS zoom to the document root, while teleported
 * fixed panels still consume the root's unscaled CSS pixels. Normalize both
 * values at the boundary so every fixed panel uses the same coordinate space.
 */
export const getDocumentZoom = (): number => {
  if (typeof document === 'undefined' || typeof window === 'undefined') return 1
  const root = document.documentElement
  const zoom = Number.parseFloat(window.getComputedStyle(root).zoom || root.style.zoom)
  return Number.isFinite(zoom) && zoom > 0 ? zoom : 1
}

export const getFixedViewport = () => {
  const zoom = getDocumentZoom()
  return {
    width: (typeof window === 'undefined' ? 0 : window.innerWidth) / zoom,
    height: (typeof window === 'undefined' ? 0 : window.innerHeight) / zoom,
    zoom
  }
}

export type FixedRect = Pick<DOMRect, 'top' | 'right' | 'bottom' | 'left' | 'width' | 'height'>

export const normalizeFixedRect = (rect: FixedRect): FixedRect => {
  const zoom = getDocumentZoom()
  if (zoom === 1) return rect
  return {
    top: rect.top / zoom,
    right: rect.right / zoom,
    bottom: rect.bottom / zoom,
    left: rect.left / zoom,
    width: rect.width / zoom,
    height: rect.height / zoom
  }
}

export interface FloatingPanelOptions {
  viewportPadding?: number
  gap?: number
  maxWidth?: number
  maxHeightRatio?: number
  mobileBreakpoint?: number
  minComfortableHeight?: number
}

/**
 * 计算挂载到 body 的浮层位置，避免触发按钮靠近视口边缘时浮层被挤到屏幕外。
 * 用于较宽的工具面板（如账号页「更多操作」）。
 */
export const getFloatingPanelPosition = (
  triggerRect: Pick<DOMRect, 'top' | 'right' | 'bottom'>,
  viewportWidth: number,
  viewportHeight: number,
  options: FloatingPanelOptions = {}
): FloatingPanelPosition => {
  const viewportPadding = options.viewportPadding ?? 16
  const gap = options.gap ?? 8
  const maxWidth = options.maxWidth ?? 320
  const maxHeightRatio = options.maxHeightRatio ?? 0.7
  const mobileBreakpoint = options.mobileBreakpoint ?? 768
  const minComfortableHeight = options.minComfortableHeight ?? 240

  const availableWidth = Math.max(0, viewportWidth - viewportPadding * 2)
  const width = Math.min(maxWidth, availableWidth)
  const left = viewportWidth < mobileBreakpoint
    ? viewportPadding
    : Math.max(
        viewportPadding,
        Math.min(triggerRect.right - width, viewportWidth - width - viewportPadding)
      )

  const preferredMaxHeight = Math.max(0, Math.floor(viewportHeight * maxHeightRatio))
  const spaceBelow = Math.max(0, viewportHeight - triggerRect.bottom - gap - viewportPadding)
  const spaceAbove = Math.max(0, triggerRect.top - gap - viewportPadding)
  const openAbove = spaceBelow < Math.min(minComfortableHeight, preferredMaxHeight) && spaceAbove > spaceBelow
  const maxHeight = Math.min(preferredMaxHeight, openAbove ? spaceAbove : spaceBelow)

  return {
    top: openAbove ? null : triggerRect.bottom + gap,
    bottom: openAbove ? viewportHeight - triggerRect.top + gap : null,
    left,
    width,
    maxHeight
  }
}

export type AnchoredMenuAlign = 'end' | 'start' | 'center'

export interface AnchoredMenuSize {
  width: number
  height: number
}

/**
 * Compact Teleport-to-body dropdown (row「更多」/ action menus).
 *
 * Prefer CSS `right` / `bottom` when pinning to the trigger so a wrong or
 * still-estimating menu width/height cannot shove the panel across the table.
 * Callers that previously used `left = rect.right - estimatedWidth` saw menus
 * land far from the trigger whenever the first measure was oversized.
 */
export interface AnchoredMenuPosition {
  top: number | null
  bottom: number | null
  left: number | null
  right: number | null
}

export interface AnchoredMenuOptions {
  padding?: number
  gap?: number
  align?: AnchoredMenuAlign
  mobileBreakpoint?: number
}

export const getAnchoredMenuPosition = (
  trigger: Pick<DOMRect, 'top' | 'right' | 'bottom' | 'left' | 'width'>,
  menu: AnchoredMenuSize,
  viewportWidth: number,
  viewportHeight: number,
  options: AnchoredMenuOptions = {}
): AnchoredMenuPosition => {
  const padding = options.padding ?? 8
  const gap = options.gap ?? 4
  const mobileBreakpoint = options.mobileBreakpoint ?? 768
  const width = Math.max(0, menu.width)
  const height = Math.max(0, menu.height)

  const align: AnchoredMenuAlign =
    options.align ??
    (viewportWidth < mobileBreakpoint ? 'center' : 'end')

  let left: number | null = null
  let right: number | null = null

  if (align === 'end') {
    // Pin menu's right edge to the trigger's right edge.
    right = Math.max(padding, viewportWidth - trigger.right)
    const menuLeft = viewportWidth - right - width
    if (menuLeft < padding) {
      right = Math.max(padding, viewportWidth - padding - width)
    }
  } else if (align === 'start') {
    left = Math.min(
      Math.max(padding, trigger.left),
      Math.max(padding, viewportWidth - width - padding)
    )
  } else {
    left = Math.min(
      Math.max(padding, trigger.left + trigger.width / 2 - width / 2),
      Math.max(padding, viewportWidth - width - padding)
    )
  }

  const spaceBelow = Math.max(0, viewportHeight - trigger.bottom - gap - padding)
  const spaceAbove = Math.max(0, trigger.top - gap - padding)
  const openAbove = height > 0 && spaceBelow < height && spaceAbove > spaceBelow

  if (openAbove) {
    // Pin menu's bottom edge just above the trigger.
    let bottom = Math.max(padding, viewportHeight - trigger.top + gap)
    const menuTop = viewportHeight - bottom - height
    if (menuTop < padding) {
      bottom = Math.max(padding, viewportHeight - padding - height)
    }
    return { top: null, bottom, left, right }
  }

  let top = trigger.bottom + gap
  if (top + height > viewportHeight - padding) {
    top = Math.max(padding, viewportHeight - height - padding)
  }
  return { top, bottom: null, left, right }
}

/** Serialize {@link AnchoredMenuPosition} for a `position: fixed` style binding. */
export const anchoredMenuStyle = (position: AnchoredMenuPosition): Record<string, string> => ({
  top: position.top == null ? 'auto' : `${position.top}px`,
  bottom: position.bottom == null ? 'auto' : `${position.bottom}px`,
  left: position.left == null ? 'auto' : `${position.left}px`,
  right: position.right == null ? 'auto' : `${position.right}px`
})
