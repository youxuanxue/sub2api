import { describe, it, expect } from 'vitest'
import { ApiError, createApiError, createNetworkError, isNetworkError } from '@/api/client.tk'

describe('ApiError', () => {
  // The root-cause fix for the app-wide "[object Object]" bug: the axios interceptor
  // now rejects with this Error subclass, so `e instanceof Error` holds everywhere.
  it('is a real Error instance carrying the flattened backend fields', () => {
    const e = createApiError({ status: 500, code: 'INTERNAL', message: 'boom', reason: 'X', error: 'detail' })
    expect(e).toBeInstanceOf(Error)
    expect(e).toBeInstanceOf(ApiError)
    expect(e.name).toBe('ApiError')
    expect(e.message).toBe('boom')
    expect(e.status).toBe(500)
    expect(e.code).toBe('INTERNAL')
    expect(e.reason).toBe('X')
    expect(e.error).toBe('detail')
  })

  it('renders its message (never "[object Object]") through String() and the Error contract', () => {
    const e = createApiError({ status: 502, code: 'BAD_GATEWAY', message: 'edge fan-out failed' })
    // The exact failure mode that produced the bug: String(e) on the old plain object.
    expect(String(e)).not.toBe('[object Object]')
    expect(String(e)).toContain('edge fan-out failed')
    expect((e as Error).message).toBe('edge fan-out failed')
  })

  it('keeps fields enumerable so spread / JSON.stringify retain them (incl. message)', () => {
    const e = createApiError({ status: 404, code: 'NOT_FOUND', message: 'missing', metadata: { id: 7 } })
    const spread = { ...e }
    expect(spread).toMatchObject({ status: 404, code: 'NOT_FOUND', message: 'missing', metadata: { id: 7 } })
    const json = JSON.parse(JSON.stringify(e))
    expect(json.message).toBe('missing')
    expect(json.status).toBe(404)
  })

  it('falls back to a default message when none is supplied', () => {
    const e = createApiError({ status: 500, code: 'INTERNAL' })
    expect(e.message).toBe('API error')
  })

  it('createNetworkError returns an ApiError that the isNetworkError duck-type guard still matches', () => {
    const e = createNetworkError('/test')
    expect(e).toBeInstanceOf(Error)
    expect(e.status).toBe(0)
    expect(e.code).toBe('NETWORK_ERROR')
    expect(e.url).toBe('/test')
    expect(isNetworkError(e)).toBe(true)
  })
})
