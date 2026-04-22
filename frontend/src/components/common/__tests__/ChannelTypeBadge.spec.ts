/**
 * ChannelTypeBadge — pins the visible "which upstream is this newapi
 * account talking to" affordance for the admin account list.
 *
 * Why this spec exists: PlatformTypeBadge labels everything as "New API"
 * for the fifth platform, so two visually-identical rows could actually be
 * a Moonshot account and a Deepseek account. Operators previously had to
 * open Edit modal to find out — losing 1 click per row at troubleshoot
 * time. The badge resolves the channel_type integer through the cached
 * useNewApiChannelTypes catalog and renders a small chip; this spec is
 * the regression guard against (a) silently dropping the chip for newapi
 * rows, (b) accidentally showing it for non-newapi rows, and (c) crashing
 * the row when the catalog hasn't loaded yet.
 */
import { describe, expect, it, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { ref, shallowRef, nextTick } from 'vue'

import ChannelTypeBadge from '../ChannelTypeBadge.vue'
import type { ChannelTypeInfo } from '@/api/admin/channels'

const mockTypes = shallowRef<ChannelTypeInfo[]>([])
const mockLoad = vi.fn(async () => {
  /* default: succeed silently; tests override via mockTypes.value */
})

vi.mock('@/composables/useNewApiChannelTypes', () => ({
  useNewApiChannelTypes: () => ({
    types: mockTypes,
    loading: ref(false),
    error: ref(null),
    load: mockLoad,
  }),
}))

beforeEach(() => {
  mockTypes.value = []
  mockLoad.mockClear()
})

describe('ChannelTypeBadge', () => {
  it('renders the resolved upstream name for a newapi account once the catalog loads', async () => {
    mockTypes.value = [
      { channel_type: 25, name: 'Moonshot', api_type: 1, has_adaptor: true, base_url: 'https://api.moonshot.cn' },
      { channel_type: 36, name: 'Deepseek', api_type: 1, has_adaptor: true, base_url: 'https://api.deepseek.com' },
    ]

    const wrapper = mount(ChannelTypeBadge, {
      props: { platform: 'newapi', channelType: 25 },
    })
    await flushPromises()
    await nextTick()

    expect(wrapper.text()).toBe('Moonshot')
    expect(mockLoad).toHaveBeenCalledTimes(1)
  })

  it('falls back to "Channel #<n>" when the catalog has no matching entry', async () => {
    mockTypes.value = [
      { channel_type: 36, name: 'Deepseek', api_type: 1, has_adaptor: true, base_url: 'https://api.deepseek.com' },
    ]

    const wrapper = mount(ChannelTypeBadge, {
      props: { platform: 'newapi', channelType: 9999 },
    })
    await flushPromises()
    await nextTick()

    expect(wrapper.text()).toBe('Channel #9999')
  })

  it('renders nothing for non-newapi platforms (avoids leaking the chip onto Anthropic/OpenAI rows)', async () => {
    mockTypes.value = [
      { channel_type: 25, name: 'Moonshot', api_type: 1, has_adaptor: true, base_url: 'https://api.moonshot.cn' },
    ]

    const wrapper = mount(ChannelTypeBadge, {
      props: { platform: 'anthropic', channelType: 25 },
    })
    await flushPromises()

    expect(wrapper.text()).toBe('')
    expect(mockLoad).not.toHaveBeenCalled()
  })

  it('renders nothing for newapi accounts whose channel_type is 0/missing (defensive)', async () => {
    const wrapper = mount(ChannelTypeBadge, {
      props: { platform: 'newapi', channelType: 0 },
    })
    await flushPromises()

    expect(wrapper.text()).toBe('')
  })
})
