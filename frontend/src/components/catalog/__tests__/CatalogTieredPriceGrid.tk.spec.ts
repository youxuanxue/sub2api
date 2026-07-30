import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import CatalogTieredPriceGrid from '../CatalogTieredPriceGrid.tk.vue'

describe('CatalogTieredPriceGrid', () => {
  it('renders tier labels in a dedicated column with aligned input/output prices', () => {
    const wrapper = mount(CatalogTieredPriceGrid, {
      props: {
        lines: [
          { label: '≤32k', inputText: '$0.024', outputText: '$0.238' },
          { label: '32k-128k', inputText: '$0.047', outputText: '$0.463' },
        ],
        inputLabel: 'Input',
        outputLabel: 'Output',
        unitLabel: '/ 1M tokens',
      },
    })

    expect(wrapper.find('[data-tk="catalog-tier-unit"]').text()).toBe('/ 1M tokens')

    const labels = wrapper.findAll('[data-tk="catalog-tier-label"]').map((n) => n.text())
    expect(labels).toEqual(['≤32k', '32k-128k'])
    const inputs = wrapper.findAll('[data-tk="catalog-tier-input"]').map((n) => n.text())
    const outputs = wrapper.findAll('[data-tk="catalog-tier-output"]').map((n) => n.text())
    expect(inputs).toEqual(['$0.024', '$0.047'])
    expect(outputs).toEqual(['$0.238', '$0.463'])
  })

  it('keeps flat rows in the same grid with unit on the header row', () => {
    const wrapper = mount(CatalogTieredPriceGrid, {
      props: {
        lines: [{ label: '', inputText: '$0.127', outputText: '$0.316' }],
        inputLabel: 'Input',
        outputLabel: 'Output',
        unitLabel: '/ 1M tokens',
      },
    })

    expect(wrapper.find('[data-tk="catalog-tier-unit"]').text()).toBe('/ 1M tokens')
    expect(wrapper.find('[data-tk="catalog-tier-label"]').text()).toBe('')
    expect(wrapper.find('[data-tk="catalog-tier-input"]').text()).toBe('$0.127')
    expect(wrapper.find('[data-tk="catalog-tier-output"]').text()).toBe('$0.316')
  })

  it('renders single-column mode for video bracket prices', () => {
    const wrapper = mount(CatalogTieredPriceGrid, {
      props: {
        mode: 'single',
        lines: [
          { label: '720p · with audio', priceText: '$0.4' },
          { label: '720p · silent', priceText: '$0.2' },
        ],
        priceLabel: 'Output',
        unitLabel: '/ second',
      },
    })

    expect(wrapper.findAll('[data-tk="catalog-tier-label"]')).toHaveLength(2)
    expect(wrapper.findAll('[data-tk="catalog-tier-price"]').map((n) => n.text())).toEqual([
      '$0.4',
      '$0.2',
    ])
  })
})
