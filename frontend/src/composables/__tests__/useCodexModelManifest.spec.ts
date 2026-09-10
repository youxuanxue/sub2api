import { defineComponent, ref } from 'vue'
import { enableAutoUnmount, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fetchCodexModelsManifest } from '@/api/codex'
import { saveAs } from 'file-saver'
import { useCodexModelManifest } from '../useCodexModelManifest'

vi.mock('@/api/codex', () => ({ fetchCodexModelsManifest: vi.fn() }))
vi.mock('file-saver', () => ({ saveAs: vi.fn() }))
enableAutoUnmount(afterEach)

function setupManifest() {
  const base = ref('https://gateway.example')
  const key = ref('key-a')
  let manifest!: ReturnType<typeof useCodexModelManifest>
  const wrapper = mount(defineComponent({
    setup() {
      manifest = useCodexModelManifest(base, key)
      return () => null
    }
  }))
  return { manifest, base, key, wrapper }
}

describe('Codex manifest lifecycle shared by key guide and quickstart', () => {
  beforeEach(() => vi.resetAllMocks())

  it.each(['key', 'base', 'unmount'] as const)('rejects late completion after %s changes', async (change) => {
    let complete!: (result: { content: string; modelCount: number }) => void
    vi.mocked(fetchCodexModelsManifest).mockImplementation(() => new Promise(resolve => { complete = resolve }))
    const state = setupManifest()
    const pending = state.manifest.load()
    const signal = vi.mocked(fetchCodexModelsManifest).mock.calls[0][2]!
    if (change === 'key') state.key.value = 'key-b'
    else if (change === 'base') state.base.value = 'https://other.example'
    else state.wrapper.unmount()
    expect(signal.aborted).toBe(true)
    complete({ content: '{"models":[{"slug":"stale-model"}]}', modelCount: 1 })
    expect(await pending).toBe(false)
    expect(state.manifest.state.value).toBe('idle')
    expect(state.manifest.content.value).toBe('')
    state.manifest.download()
    expect(saveAs).not.toHaveBeenCalled()
  })

  it('keeps the latest request when an aborted request completes last', async () => {
    let complete!: (result: { content: string; modelCount: number }) => void
    vi.mocked(fetchCodexModelsManifest)
      .mockImplementationOnce(() => new Promise(resolve => { complete = resolve }))
      .mockResolvedValueOnce({ content: '{"models":[]}', modelCount: 0 })
    const { manifest } = setupManifest()
    const first = manifest.load()
    expect(await manifest.load()).toBe(true)
    complete({ content: '{"models":[{"slug":"stale-model"}]}', modelCount: 1 })
    expect(await first).toBe(false)
    expect(manifest.content.value).toBe('{"models":[]}')
    manifest.download()
    expect(saveAs).toHaveBeenCalledWith(expect.any(Blob), 'codex-models.json')
  })

  it('blocks download after errors and recovers on retry', async () => {
    vi.mocked(fetchCodexModelsManifest)
      .mockRejectedValueOnce(new Error('network unavailable'))
      .mockResolvedValueOnce({ content: '{"models":[]}', modelCount: 0 })
    const { manifest, key } = setupManifest()
    expect(await manifest.load()).toBe(false)
    expect(manifest.state.value).toBe('error')
    manifest.download()
    expect(saveAs).not.toHaveBeenCalled()
    expect(await manifest.load()).toBe(true)
    expect(manifest.state.value).toBe('ready')
    key.value = ''
    expect(await manifest.load()).toBe(false)
    expect(fetchCodexModelsManifest).toHaveBeenCalledTimes(2)
    expect(manifest.models.value).toBeNull()
  })
})
