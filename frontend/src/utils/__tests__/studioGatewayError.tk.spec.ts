import { describe, expect, it } from 'vitest'
import { classifyGatewayError, parseGatewayErrorMessage } from '@/utils/studioGatewayError.tk'

describe('studioGatewayError', () => {
  it('parses JSON gateway error bodies', () => {
    const raw =
      '{"message":"The \'gemini-3.1-flash-image\' model is not supported when using Codex with a ChatGPT account.","type":"invalid_request_error"}'
    expect(parseGatewayErrorMessage(raw)).toBe(
      "The 'gemini-3.1-flash-image' model is not supported when using Codex with a ChatGPT account."
    )
    expect(classifyGatewayError(parseGatewayErrorMessage(raw))).toBe('unsupported_model')
  })
})
