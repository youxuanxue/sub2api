import { describe, expect, it } from 'vitest'
import {
  anchoredMenuStyle,
  getAnchoredMenuPosition,
  getFloatingPanelPosition
} from '@/utils/floatingPanel'

describe('getFloatingPanelPosition', () => {
  it('移动端使用视口安全边距，不再从靠左按钮向屏幕外展开', () => {
    const position = getFloatingPanelPosition(
      { top: 160, right: 148, bottom: 200 },
      393,
      844
    )

    expect(position).toMatchObject({
      top: 208,
      bottom: null,
      left: 16,
      width: 320
    })
    expect(position.left + position.width).toBeLessThanOrEqual(393 - 16)
  })

  it('桌面端与按钮右侧对齐', () => {
    const position = getFloatingPanelPosition(
      { top: 100, right: 1000, bottom: 140 },
      1280,
      900
    )

    expect(position.left).toBe(680)
    expect(position.width).toBe(320)
  })

  it('按钮下方空间不足时改为向上展开', () => {
    const position = getFloatingPanelPosition(
      { top: 700, right: 1000, bottom: 740 },
      1280,
      800
    )

    expect(position.top).toBeNull()
    expect(position.bottom).toBe(108)
    expect(position.maxHeight).toBe(560)
  })
})

describe('getAnchoredMenuPosition', () => {
  it('桌面端用 right 锚定触发器右缘，避免 left=right-width 估算漂移', () => {
    const position = getAnchoredMenuPosition(
      { top: 400, right: 1200, bottom: 430, left: 1140, width: 60 },
      { width: 208, height: 200 },
      1280,
      900
    )

    expect(position).toMatchObject({
      top: 434,
      bottom: null,
      left: null,
      right: 80
    })
    // Menu right edge == trigger right edge
    expect(1280 - (position.right ?? 0)).toBe(1200)
  })

  it('靠近视口底部时改用 bottom 向上展开，不依赖 height 估算去减 top', () => {
    const position = getAnchoredMenuPosition(
      { top: 720, right: 1200, bottom: 750, left: 1140, width: 60 },
      { width: 208, height: 240 },
      1280,
      800
    )

    expect(position.top).toBeNull()
    expect(position.bottom).toBe(800 - 720 + 4)
    expect(position.right).toBe(80)
  })

  it('即便传入偏大的 width，右缘仍钉在触发器上（复现「菜单飞到表格中间」）', () => {
    const position = getAnchoredMenuPosition(
      { top: 400, right: 1200, bottom: 430, left: 1140, width: 60 },
      { width: 800, height: 200 },
      1280,
      900
    )

    // Old formula left = 1200 - 800 = 400 would place the menu mid-table.
    // right-anchoring keeps the menu's right edge on the trigger.
    expect(position.left).toBeNull()
    expect(position.right).toBe(80)
    expect(anchoredMenuStyle(position)).toMatchObject({
      top: '434px',
      bottom: 'auto',
      left: 'auto',
      right: '80px'
    })
  })

  it('移动端居中对齐触发器', () => {
    const position = getAnchoredMenuPosition(
      { top: 200, right: 200, bottom: 232, left: 140, width: 60 },
      { width: 208, height: 200 },
      390,
      800
    )

    expect(position.right).toBeNull()
    expect(position.left).toBeGreaterThanOrEqual(8)
    expect((position.left ?? 0) + 208).toBeLessThanOrEqual(390 - 8)
  })
})
