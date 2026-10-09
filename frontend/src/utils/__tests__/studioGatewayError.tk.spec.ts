import { describe, expect, it } from 'vitest'
import {
  classifyGatewayError,
  parseGatewayErrorMessage,
  studioErrorShowsTopUp
} from '@/utils/studioGatewayError.tk'

describe('studioGatewayError', () => {
  it('parses JSON gateway error bodies', () => {
    const raw =
      '{"message":"The \'gemini-3.1-flash-image\' model is not supported when using Codex with a ChatGPT account.","type":"invalid_request_error"}'
    expect(parseGatewayErrorMessage(raw)).toBe(
      "The 'gemini-3.1-flash-image' model is not supported when using Codex with a ChatGPT account."
    )
    expect(classifyGatewayError(parseGatewayErrorMessage(raw))).toBe('unsupported_model')
  })

  it('classifies trial unpaid media 402 for Studio top-up CTA', () => {
    const raw =
      '{"message":"Image and video generation require a completed recharge. Text models remain available on your trial balance.","type":"invalid_request_error","code":"trial_unpaid_media_blocked"}'
    expect(classifyGatewayError(raw)).toBe('trial_unpaid_media')
    expect(studioErrorShowsTopUp('trial_unpaid_media')).toBe(true)
    expect(studioErrorShowsTopUp('insufficient_balance')).toBe(true)
    expect(studioErrorShowsTopUp('generic')).toBe(false)
  })
})
