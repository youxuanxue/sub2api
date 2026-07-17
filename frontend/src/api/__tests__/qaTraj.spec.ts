import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import type { AxiosInstance, AxiosRequestConfig } from 'axios'

vi.mock('@/i18n', () => ({
  getLocale: () => 'zh-CN'
}))

describe('qaTraj API', () => {
  let apiClient: AxiosInstance
  let qaTraj: typeof import('@/api/qaTraj')

  beforeEach(async () => {
    vi.resetModules()
    const clientMod = await import('@/api/client')
    apiClient = clientMod.apiClient
    qaTraj = await import('@/api/qaTraj')
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('exportKey posts the per-key v2 payload and returns the async job', async () => {
    let captured: AxiosRequestConfig | undefined
    apiClient.defaults.adapter = vi.fn().mockImplementation((config: AxiosRequestConfig) => {
      captured = config
      return Promise.resolve({
        status: 200,
        statusText: 'OK',
        headers: {},
        config,
        data: { code: 0, data: { job_id: 'job-123', status: 'pending', record_count: 0 } }
      })
    })

    const job = await qaTraj.qaTrajAPI.exportKey(42)

    expect(captured?.url).toBe('/users/me/qa/traj/export')
    expect(captured?.method?.toLowerCase()).toBe('post')
    expect(JSON.parse(captured?.data as string)).toEqual({ api_key_id: 42, format: 'v2' })
    expect(job.job_id).toBe('job-123')
    expect(job.status).toBe('pending')
  })

  it('getJob polls the job endpoint and returns the ready download url', async () => {
    let captured: AxiosRequestConfig | undefined
    apiClient.defaults.adapter = vi.fn().mockImplementation((config: AxiosRequestConfig) => {
      captured = config
      return Promise.resolve({
        status: 200,
        statusText: 'OK',
        headers: {},
        config,
        data: {
          code: 0,
          data: {
            job_id: 'job-123',
            status: 'done',
            download_url: '/api/v1/users/me/qa/traj/exports/k.zip',
            record_count: 3
          }
        }
      })
    })

    const job = await qaTraj.qaTrajAPI.getJob('job-123')

    expect(captured?.url).toBe('/users/me/qa/traj/export/jobs/job-123')
    expect(captured?.method?.toLowerCase()).toBe('get')
    expect(job.status).toBe('done')
    expect(job.record_count).toBe(3)
    expect(job.download_url).toBe('/api/v1/users/me/qa/traj/exports/k.zip')
  })
})
