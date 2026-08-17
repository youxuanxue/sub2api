import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AxiosInstance, AxiosRequestConfig } from 'axios'

vi.mock('@/i18n', () => ({ getLocale: () => 'zh-CN' }))

describe('qaBundle API', () => {
  let apiClient: AxiosInstance
  let qaBundle: typeof import('@/api/qaBundle')

  beforeEach(async () => {
    vi.resetModules()
    apiClient = (await import('@/api/client')).apiClient
    qaBundle = await import('@/api/qaBundle')
  })

  afterEach(() => vi.restoreAllMocks())

  it('creates one scoped S3 bundle job for the selected API key', async () => {
    let captured: AxiosRequestConfig | undefined
    apiClient.defaults.adapter = vi.fn().mockImplementation((config: AxiosRequestConfig) => {
      captured = config
      return Promise.resolve({
        status: 200, statusText: 'OK', headers: {}, config,
        data: { code: 0, data: { job_id: 'bundle-1', status: 'pending', api_key_id: 42 } }
      })
    })

    const job = await qaBundle.qaBundleAPI.createBundle(42)

    expect(captured?.url).toBe('/users/me/qa/bundles')
    expect(captured?.method?.toLowerCase()).toBe('post')
    expect(JSON.parse(captured?.data as string)).toEqual({ api_key_id: 42 })
    expect(job.job_id).toBe('bundle-1')
  })

  it('creates ZIP from the committed bundle job, not from raw QA', async () => {
    let captured: AxiosRequestConfig | undefined
    apiClient.defaults.adapter = vi.fn().mockImplementation((config: AxiosRequestConfig) => {
      captured = config
      return Promise.resolve({
        status: 200, statusText: 'OK', headers: {}, config,
        data: { code: 0, data: { job_id: 'zip-1', bundle_job_id: 'bundle-1', status: 'pending' } }
      })
    })

    await qaBundle.qaBundleAPI.createExport('bundle-1')

    expect(captured?.url).toBe('/users/me/qa/bundles/bundle-1/export')
    expect(captured?.method?.toLowerCase()).toBe('post')
  })
})
