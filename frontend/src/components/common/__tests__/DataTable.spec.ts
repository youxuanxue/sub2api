import { mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import DataTable from '../DataTable.vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key
  })
}))

const stubDesktopMatchMedia = () => {
  Object.defineProperty(window, 'matchMedia', {
    writable: true,
    value: vi.fn().mockImplementation((query: string) => ({
      matches: true,
      media: query,
      onchange: null,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      addListener: vi.fn(),
      removeListener: vi.fn(),
      dispatchEvent: vi.fn()
    }))
  })
}

const stubMobileMatchMedia = () => {
  Object.defineProperty(window, 'matchMedia', {
    writable: true,
    value: vi.fn().mockImplementation((query: string) => ({
      matches: false,
      media: query,
      onchange: null,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      addListener: vi.fn(),
      removeListener: vi.fn(),
      dispatchEvent: vi.fn()
    }))
  })
}

describe('DataTable', () => {
  afterEach(() => vi.restoreAllMocks())
  beforeEach(() => {
    stubDesktopMatchMedia()
    localStorage.clear()
  })

  it('renders paired sort arrows and highlights the active direction', async () => {
    const wrapper = mount(DataTable, {
      props: {
        columns: [
          { key: 'name', label: 'Name', sortable: true },
          { key: 'created_at', label: 'Created', sortable: true }
        ],
        data: [
          { id: 1, name: 'Beta', created_at: '2026-01-02T00:00:00Z' },
          { id: 2, name: 'Alpha', created_at: '2026-01-01T00:00:00Z' }
        ],
        defaultSortKey: 'name',
        defaultSortOrder: 'asc'
      },
      slots: {
        'header-created_at': '<span data-test="custom-created-header">Created</span>'
      }
    })

    await wrapper.vm.$nextTick()

    const nameHeader = wrapper.findAll('th')[0]
    expect(wrapper.findAll('th')[1].find('[data-test="custom-created-header"]').exists()).toBe(true)
    expect(nameHeader.attributes('aria-sort')).toBe('ascending')
    expect(nameHeader.findAll('svg')).toHaveLength(2)
    expect(nameHeader.findAll('svg')[0].classes()).toContain('text-primary-600')
    expect(nameHeader.findAll('svg')[1].classes()).toContain('text-gray-300')

    await nameHeader.trigger('click')
    await wrapper.vm.$nextTick()

    expect(nameHeader.attributes('aria-sort')).toBe('descending')
    expect(nameHeader.findAll('svg')[0].classes()).toContain('text-gray-300')
    expect(nameHeader.findAll('svg')[1].classes()).toContain('text-primary-600')
  })

  it('renders every row with no virtual padding spacer for small datasets (virtualization off)', async () => {
    const data = Array.from({ length: 8 }, (_, i) => ({ id: i + 1, name: `Row ${i + 1}` }))
    const wrapper = mount(DataTable, {
      props: {
        columns: [{ key: 'name', label: 'Name' }],
        data
      }
    })

    await wrapper.vm.$nextTick()

    // Virtualization is OFF for a small list…
    expect((wrapper.vm as any).shouldVirtualize).toBe(false)
    // …every row is in the DOM…
    expect(wrapper.findAll('tbody tr[data-index]')).toHaveLength(data.length)
    // …and there are no aria-hidden virtual padding spacer rows.
    expect(wrapper.findAll('tbody tr[aria-hidden="true"]')).toHaveLength(0)
  })

  it('switches to windowed rendering once row count exceeds virtualizeThreshold', async () => {
    const data = Array.from({ length: 12 }, (_, i) => ({ id: i + 1, name: `Row ${i + 1}` }))
    const wrapper = mount(DataTable, {
      props: {
        columns: [{ key: 'name', label: 'Name' }],
        data,
        virtualizeThreshold: 3
      }
    })

    await wrapper.vm.$nextTick()

    // Virtualization is ON: the mode-switch decision flipped…
    expect((wrapper.vm as any).shouldVirtualize).toBe(true)
    // …and the virtualizer drives off the full row count.
    const exposed = (wrapper.vm as any).virtualizer
    const instance = exposed?.value ?? exposed
    expect(instance.options.count).toBe(data.length)
  })

  it.each(['stable', 'missing', 'duplicate'] as const)(
    'keeps measurements with rows and drops old pages with %s keys',
    async (keys) => {
      vi.spyOn(HTMLElement.prototype, 'offsetHeight', 'get').mockImplementation(function (this: HTMLElement) {
        return this.tagName === 'TR' ? (this.textContent?.includes('First 0') ? 156 : 56) : 800
      })
      const page = (prefix: string, offset = 0) => Array.from({ length: 12 }, (_, i) => ({
        ...(keys === 'missing' ? {} : { id: keys === 'duplicate' ? i % 2 : offset + i }),
        name: `${prefix} ${i}`
      }))
      const firstPage = page('First')
      const wrapper = mount(DataTable, {
        props: {
          columns: [{ key: 'name', label: 'Name' }],
          data: firstPage, virtualizeThreshold: 1, estimateRowHeight: 56
        }
      })
      await wrapper.vm.$nextTick()
      const exposed = (wrapper.vm as any).virtualizer
      const instance = exposed?.value ?? exposed
      const firstKey = instance.options.getItemKey(0)
      expect(new Set(firstPage.map((_, i) => instance.options.getItemKey(i))).size).toBe(12)

      instance.resizeItem(0, 156)
      expect(instance.getTotalSize()).toBe(12 * 56 + 100)

      await wrapper.setProps({ data: [...firstPage].reverse() })
      expect(instance.options.getItemKey(11)).toBe(firstKey)
      expect(instance.getTotalSize()).toBe(12 * 56 + 100)

      for (let index = 1; index <= 3; index++) {
        await wrapper.setProps({ data: page('Next', index * 12) })
        await wrapper.vm.$nextTick()
        expect(instance.getTotalSize()).toBe(12 * 56)
        expect(wrapper.text()).not.toContain('First')
        // Observe retention; seed measurements only through the public API.
        expect(instance.itemSizeCache.size).toBeLessThanOrEqual(12)
        instance.resizeItem(0, 156)
      }
      wrapper.unmount()
    }
  )

  it('emits controlled current-page selection while preserving off-page keys', async () => {
    const wrapper = mount(DataTable, {
      props: {
        columns: [{ key: 'name', label: 'Name' }],
        data: [
          { id: 1, name: 'One' },
          { id: 2, name: 'Two' }
        ],
        rowKey: 'id',
        selectable: true,
        selectedKeys: [99]
      }
    })

    await wrapper.get('[data-test="select-all"]').setValue(true)

    const selectedAll = wrapper.emitted('update:selectedKeys')?.at(-1)?.[0]
    expect(selectedAll).toEqual([99, 1, 2])

    await wrapper.setProps({ selectedKeys: selectedAll as number[] })
    const rowCheckboxes = wrapper.findAll<HTMLInputElement>('[data-test="select-row"]')
    expect(rowCheckboxes.every((checkbox) => checkbox.element.checked)).toBe(true)

    await rowCheckboxes[0].setValue(false)

    expect(wrapper.emitted('update:selectedKeys')?.at(-1)?.[0]).toEqual([99, 2])
    expect(wrapper.emitted('selectionChange')?.at(-1)?.[0]).toEqual([99, 2])
  })

  it('remeasures expanded details and survives filtering across the virtual threshold', async () => {
    vi.spyOn(HTMLElement.prototype, 'offsetHeight', 'get').mockImplementation(function (this: HTMLElement) {
      return this.tagName === 'TR' ? 56 : 800
    })
    const data = Array.from({ length: 12 }, (_, id) => ({ id, name: `Row ${id}` }))
    const wrapper = mount(DataTable, {
      props: {
        columns: [{ key: 'name', label: 'Name' }], data,
        virtualizeThreshold: 3, estimateRowHeight: 56,
        expandable: () => true, expandedKeys: new Set<number>(),
      },
      slots: { 'row-detail': '<div>Expanded account details</div>' },
    })
    await wrapper.vm.$nextTick()
    const exposed = (wrapper.vm as any).virtualizer
    const instance = exposed?.value ?? exposed
    await wrapper.setProps({ expandedKeys: new Set([0]) })
    expect(wrapper.text()).toContain('Expanded account details')
    instance.resizeItem(1, 300)
    expect(instance.getTotalSize()).toBe(12 * 56 + 300)

    await wrapper.setProps({ expandedKeys: new Set() })
    expect(wrapper.text()).not.toContain('Expanded account details')
    expect(instance.getTotalSize()).toBe(12 * 56)

    await wrapper.setProps({ data: data.slice(0, 2) })
    expect(wrapper.findAll('tbody tr[data-index]')).toHaveLength(2)
    expect(wrapper.findAll('tbody tr[aria-hidden="true"]')).toHaveLength(0)
    await wrapper.setProps({ data })
    expect(instance.getTotalSize()).toBe(12 * 56)
    expect(wrapper.text()).toContain('Row 0')
    wrapper.unmount()
  })

  it('keeps the single usage field shrinkable in a 320px mobile card', () => {
    stubMobileMatchMedia()
    const viewport = document.createElement('div')
    viewport.style.width = '320px'
    document.body.appendChild(viewport)
    const wrapper = mount(DataTable, {
      attachTo: viewport,
      props: {
        columns: [{ key: 'usage', label: 'Usage' }],
        data: [{ id: 1, usage: 'snapshot' }],
        rowKey: 'id'
      },
      slots: {
        'cell-usage': '<div data-test="usage-cell">snapshot</div>'
      }
    })

    expect(viewport.style.width).toBe('320px')
    expect(wrapper.findAll('[data-field="usage"]')).toHaveLength(1)
    expect(wrapper.find('[data-field="ollama_cloud_usage"]').exists()).toBe(false)
    const field = wrapper.get('[data-field="usage"]')
    expect(field.classes()).toContain('min-w-0')
    expect(field.get('div').classes()).toEqual(expect.arrayContaining(['min-w-0', 'max-w-full']))
    expect(wrapper.findAll('[data-test="usage-cell"]')).toHaveLength(1)

    wrapper.unmount()
    viewport.remove()
  })

  it('offers current-page select all in the mobile card layout', async () => {
    stubMobileMatchMedia()
    const wrapper = mount(DataTable, {
      props: {
        columns: [{ key: 'name', label: 'Name' }],
        data: [
          { id: 1, name: 'One' },
          { id: 2, name: 'Two' }
        ],
        rowKey: 'id',
        selectable: true,
        selectedKeys: [99]
      }
    })

    await wrapper.get('[data-test="select-all-mobile"]').setValue(true)

    expect(wrapper.emitted('update:selectedKeys')?.at(-1)?.[0]).toEqual([99, 1, 2])
  })
})
