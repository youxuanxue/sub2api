import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { defineComponent, nextTick } from 'vue'
import VertexServiceAccountFields from '../VertexServiceAccountFields.vue'
import { useVertexServiceAccountFields } from '@/composables/useVertexServiceAccountFields'

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn(),
    showInfo: vi.fn()
  })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

const IconStub = defineComponent({ template: '<span />' })

const SAMPLE_SA = JSON.stringify({
  type: 'service_account',
  project_id: 'proj-1',
  private_key: '-----BEGIN PRIVATE KEY-----\nabc\n-----END PRIVATE KEY-----\n',
  client_email: 'sa@proj-1.iam.gserviceaccount.com'
})

describe('VertexServiceAccountFields', () => {
  it('paste JSON auto-fills project id on change', async () => {
    const Host = defineComponent({
      components: { VertexServiceAccountFields },
      setup() {
        const fields = useVertexServiceAccountFields()
        return { fields }
      },
      template: '<VertexServiceAccountFields :fields="fields" variant="create" />'
    })

    const wrapper = mount(Host, {
      global: { stubs: { Icon: IconStub } }
    })

    const textarea = wrapper.find('textarea')
    await textarea.setValue(SAMPLE_SA)
    await textarea.trigger('change')
    await nextTick()

    expect(wrapper.vm.fields.projectId.value).toBe('proj-1')
    expect(wrapper.vm.fields.clientEmail.value).toBe('sa@proj-1.iam.gserviceaccount.com')
  })
})
