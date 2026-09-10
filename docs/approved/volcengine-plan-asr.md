---
title: VolcEngine Agent Plan Speech Recognition
status: approved
approved_by: "user (conversation approval of revised design, 2026-09-09)"
approved_at: "2026-09-09"
authors: [codex]
created: 2026-09-09
related_stories: []
---

# VolcEngine Agent Plan Speech Recognition

## Decision

Add OpenAI-compatible `POST /v1/audio/transcriptions` for uploaded recordings.
Keep the provider's binary WebSocket protocol internal. A public realtime
WebSocket protocol is a separate API contract and is not implied by this endpoint.

Accept MP3 and integer PCM WAV recordings, mono or stereo, at 8-48 kHz.
Normalize inside the gateway to mono PCM16 at 16 kHz using pinned pure-Go
decoders and resampling. No external conversion is required for default TTS MP3.
Limit upload size to 25 MiB and decoded recording duration to 60 seconds.
Normalization runs under the user concurrency slot and a 10-second deadline;
reject unsupported encoding before selecting an execution account or opening a connection.
The handler has a 90-second total deadline including normalization, admission,
handshakes and sending. Handshakes have a 12-second sub-deadline. The 60-second
cap leaves headroom over the measured 61.983-second near-limit upstream execution.
The multipart fields are `model`, `file`, and `response_format=json|text`.
JSON output is `{"text":"..."}`. Streaming transcription output, translations,
timestamps and additional audio containers require a later capability extension.

## Integration

- Share the existing audio speech handler's scheduling, balance hold,
  account slot, retry, and usage submission flow through one audio execution owner.
  Speech and transcription supply their input validation and forwarding functions.
- Select only authorized, mapped VolcEngine Agent Plan accounts. Preserve cooldowns.
- Use `wss://openspeech.bytedance.com/api/v3/plan/sauc/bigmodel_nostream`,
  `X-Api-Key`, and `X-Api-Resource-Id: volc.seedasr.sauc.duration`.
- Use the existing WebSocket library and account proxy transport. Read the initial
  acknowledgement before sending PCM in 200 ms chunks at 200 ms intervals. Run one reader and one
  writer; cancellation closes both. Bound compressed and decompressed payloads.
- Check binary frame sizes, sequence flags, provider errors and terminal status.
  Do not declare successful transcription from a handshake or interim result.
- Retry account selection only on an unsuccessful HTTP handshake. Once audio is
  sent, failures never replay the recording. Join the sender on every exit;
  cancellation and timeouts release account/user slots and unsettled holds.
- Direct and Universal keys use the candidate eligibility owner. Audio requests
  have explicit endpoint shapes and multipart model extraction; media never
  inherits a text protocol Plan from another ingress context.
- Log request identity, status and duration; do not log uploaded audio or credentials.
- Retain the existing text protocol router boundary: media protocols are outside
  `protocolrouter` under `protocol-routing-ssot.md` section 2.

## Billing

Official source: <https://www.volcengine.com/docs/82379/2516284>, verified 2026-09-09:
`doubao-seed-asr-2.0` costs CNY 1/hour. Store the rate in the pricing registry with
the existing CNY/USD conversion and tax policy. Extend the existing audio price
owner to accept this model's per-second price rather than use a Grok fallback.

The balance hold uses normalized PCM duration. Settlement uses the final provider
`audio_info.duration` in milliseconds, converted to the billing owner's units.
An empty transcription can be valid for silence, but must have positive observed
audio duration and an explicit successful terminal response. Failed or incomplete
requests do not produce a successful usage row. Final duration must match the
submitted normalized recording to within one millisecond. If a completed result
cannot reach a disconnected client, preserve the completed usage for settlement;
an incomplete execution releases the hold, even if the provider incurred cost.

The manifest and generated bundle describe the prepared capability; they do not
activate runtime mappings. After deployment, use fresh upstream/price evidence
and the existing reviewed activation workflow for a limited account rollout,
verify gateway attribution and billed duration, then promote the remaining scope.

## Prototype Evidence

On 2026-09-09, a healthy Plan account completed TTS-to-ASR roundtrip through the
production host's egress. TTS generated 95,946 PCM bytes at 16 kHz mono (2998.3125 ms).
ASR returned `audio_info.duration=2998` and exactly:

```text
Hello, this is a short audio test.
```

The first MP3 experiment at 24 kHz returned no text and duration zero; that is not
servability evidence. The passing PCM experiment followed the documented 16 kHz
requirement and acknowledgement/chunk sequencing. Do not infer which difference
caused the first result without an isolated comparison.

API and binary protocol sources:
<https://www.volcengine.com/docs/82379/2516286>
<https://www.volcengine.com/docs/6561/1354869>

## Acceptance

The revised 2026-09-09 probe used the production account egress and the gateway's
Go-normalized default 24 kHz MP3 fixture. It returned the original text with
2664 ms duration in 5.073 seconds. A 59800 ms recording returned exactly 59800 ms
in 61.983 seconds. A 200 ms silent recording returned empty text and 200 ms in
2.123 seconds. These are direct upstream proofs, not deployed gateway acceptance.

Test default TTS MP3, WAV resampling and amplitude preservation, malformed RIFF/chunks, unsupported sample rates, size and duration
limits, missing auth, unsupported models, wrong provider, provider errors,
truncated/gzip-bomb frames, cancellation, silent input, and exact millisecond cost.
Perform a reserved gateway probe and verify account attribution and billed duration
after the limited activation and before promotion to remaining accounts.
