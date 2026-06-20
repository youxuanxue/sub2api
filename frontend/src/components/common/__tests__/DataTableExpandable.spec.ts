import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import DataTable from '@/components/common/DataTable.vue'
import type { Column } from '@/components/common/types'

// Stub i18n (DataTable's empty-state uses t()).
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

// The test runs in jsdom where matchMedia.matches === false (see test setup), so
// DataTable renders its NON-virtualized mobile card path — deterministic, no
// @tanstack/vue-virtual layout needed. The desktop virtualized path shares the
// same expandable predicate + expandedKeys contract (the expand DECISION itself is
// unit-tested in accountsEdgePanelsTk.spec.ts via isStubPanelExpanded).

const columns: Column[] = [{ key: 'name', label: 'Name' }]
const data = [
  { id: 1, name: 'stub-a', expandable: true },
  { id: 2, name: 'plain-b', expandable: false },
  { id: 3, name: 'stub-c', expandable: true }
]

function mountTable(props: Record<string, unknown>) {
  return mount(DataTable, {
    attachTo: document.body,
    props: { columns, data, ...props },
    slots: {
      'cell-name': '<template #cell-name="{ row }"><span>{{ row.name }}</span></template>',
      'row-detail': '<template #row-detail="{ row }"><div class="edge-detail" :data-edge="row.id">detail-{{ row.id }}</div></template>'
    }
  })
}

describe('DataTable expandable detail rows', () => {
  it('renders NO detail rows when expandable is not provided (backward compatible)', () => {
    const wrapper = mountTable({})
    expect(wrapper.findAll('.edge-detail')).toHaveLength(0)
    wrapper.unmount()
  })

  it('renders a detail row only for expandable rows whose key is in expandedKeys', () => {
    const wrapper = mountTable({
      expandable: (row: { expandable?: boolean }) => row.expandable === true,
      expandedKeys: new Set([1, 3])
    })
    const details = wrapper.findAll('.edge-detail')
    expect(details).toHaveLength(2)
    expect(details.map((d) => d.attributes('data-edge'))).toEqual(['1', '3'])
    wrapper.unmount()
  })

  it('does NOT render a detail row for a non-expandable row even if its key is in expandedKeys', () => {
    const wrapper = mountTable({
      expandable: (row: { expandable?: boolean }) => row.expandable === true,
      expandedKeys: new Set([2]) // row 2 is expandable:false
    })
    expect(wrapper.findAll('.edge-detail')).toHaveLength(0)
    wrapper.unmount()
  })

  it('renders no detail rows when expandedKeys is empty', () => {
    const wrapper = mountTable({
      expandable: () => true,
      expandedKeys: new Set()
    })
    expect(wrapper.findAll('.edge-detail')).toHaveLength(0)
    wrapper.unmount()
  })
})
