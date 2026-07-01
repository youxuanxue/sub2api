import { describe, expect, it } from 'vitest'
import {
  buildDataVideoUri,
  normalizeVideoBase64Payload,
  normalizeVideoMimeForDataUri,
  parseDataVideoUri,
} from '../studioInlineVideo.tk'

describe('normalizeVideoMimeForDataUri', () => {
  it('strips codec parameters from upstream mimeType', () => {
    expect(normalizeVideoMimeForDataUri('video/mp4; codecs=avc1.640028')).toBe('video/mp4')
  })

  it('maps encoding=base64 to video/mp4 when mime is absent', () => {
    expect(normalizeVideoMimeForDataUri('', 'base64')).toBe('video/mp4')
    expect(normalizeVideoMimeForDataUri('', 'mp4')).toBe('video/mp4')
  })
})

describe('normalizeVideoBase64Payload', () => {
  it('removes whitespace and fixes url-safe alphabet', () => {
    expect(normalizeVideoBase64Payload('a_b-c')).toBe('a/b+c===')
  })
})

describe('buildDataVideoUri + parseDataVideoUri', () => {
  it('round-trips a Veo-shaped inline clip', () => {
    const uri = buildDataVideoUri('video/mp4; codecs=avc1', 'QUJD', 'BASE64')
    expect(uri).toBe('data:video/mp4;base64,QUJD')
    expect(parseDataVideoUri(uri)).toEqual({ mime: 'video/mp4', base64: 'QUJD' })
  })

  it('parses data URIs that already carry codec parameters before base64', () => {
    const uri = 'data:video/mp4; codecs=avc1;base64,QUJD'
    expect(parseDataVideoUri(uri)).toEqual({ mime: 'video/mp4', base64: 'QUJD' })
  })
})
