import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import PlatformIcon from '../PlatformIcon.vue'

describe('PlatformIcon execution-platform boundary', () => {
  it('keeps Antigravity distinct from Gemini for admin diagnostics', () => {
    const gemini = mount(PlatformIcon, { props: { platform: 'gemini' } })
    const antigravity = mount(PlatformIcon, { props: { platform: 'antigravity' } })

    expect(gemini.get('path').attributes('d')).toContain('M12 2')
    expect(antigravity.get('path').attributes('d')).toContain('M19.35 10.04')
    expect(antigravity.get('path').attributes('d')).not.toBe(gemini.get('path').attributes('d'))
  })
})
