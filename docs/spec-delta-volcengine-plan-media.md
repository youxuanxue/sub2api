# VolcEngine Agent Plan Aliases and Media

## Background

Gateway probes on 2026-09-09 showed that five historical Plan model names returned
newer models. Model identity and settlement should describe the served target.
Plan media requires dedicated paths that the generic New API adapter does not use.

## Delta

- Plan-only aliases: Seed 2.0 Code/Pro -> Seed 2.1 Turbo, GLM 5.2 -> GLM 5.3,
  Kimi 2.6 -> Kimi 2.7 Code, MiniMax 2.7 -> MiniMax 3. Flatten the existing
  GLM 4.5 Air alias to GLM 5.3. Other provider scopes retain their prior intent.
- Add `doubao-embedding-vision` and `doubao-seedream-5.0-lite` to Plan intent.
  Forward using the same native path as Plan chat/responses; retain the Plan root.
  Text embedding inputs use `/embeddings`; object parts use `/embeddings/multimodal`.
  Normalize the latter's single-vector object to OpenAI's data array, preserving usage.
- Lite price is CNY 0.22 per successfully returned image. Its price is independently
  verified, not inherited from a different Seedream version. Studio uses existing
  Seedream size controls. This change does not expose provider streaming image APIs.
- Add `doubao-seed-tts-2.0` via existing `/v1/audio/speech`. The native Plan HTTP
  endpoint uses `X-Api-Key` and `seed-tts-2.0`, returning newline-delimited JSON
  chunks containing base64 audio. Return audio after explicit completion and meter
  authoritative `usage.text_words`. Supported output formats are MP3, PCM, Opus;
  WAV is rejected because this upstream emits repeated WAV headers per chunk.
- ASR now accepts uploaded MP3/PCM WAV through `/v1/audio/transcriptions`, normalizes
  to 16 kHz mono PCM, and settles on final milliseconds. Its new
  public ingress and duration settlement design is in `approved/volcengine-plan-asr.md`.

## Scenarios and Evidence

Healthy Plan account 141, through production egress, 2026-09-09:

| Case | Result | Metering evidence |
| --- | --- | --- |
| Text embeddings | HTTP 200, finite 1024-d vector | 24 input tokens |
| Text + image embeddings | HTTP 200, finite 1024-d vector | 27 text + 1312 image tokens |
| Seedream Lite | HTTP 200, validated PNG, 2048 x 2048 | 1 generated image |
| TTS HTTP | Complete MP3 audio, 22,701 bytes | 34 billable characters |
| TTS PCM -> ASR WS | Exact text roundtrip | 95,946 PCM bytes, 2998 ms provider duration |
| Default 24 kHz TTS MP3 -> normalized ASR | Exact text, 5.073 s elapsed | 2664 ms provider duration |
| Near-limit recording -> ASR | Recognized text, 61.983 s elapsed | 59800 ms provider duration |
| Silence -> ASR | Valid empty text, 2.123 s elapsed | 200 ms provider duration |

Official API evidence:
<https://www.volcengine.com/docs/82379/2375464>
<https://www.volcengine.com/docs/82379/2375486>
<https://www.volcengine.com/docs/82379/2516286>

Official price evidence:
<https://www.volcengine.com/docs/82379/1544106>
<https://www.volcengine.com/docs/82379/2516284>

## Validation and Rollout

Focused service tests cover alias target settlement, provider isolation, Plan-only
URLs, text/image usage, actual image counts, TTS payloads, authoritative character
cost, incomplete responses, and invalid audio encodings. Sentinel hooks protect
the forwarding and alias owners from silent upstream-merge removal.

These upstream probes do not prove the deployed gateway path. Before activation:
merge and deploy the code, publish the registry price, generate/review the bundle,
then run `modelops activate` with independent evidence. Follow with reserved gateway
probes and usage-log attribution. Existing cooled accounts remain cooled; never
clear cooldowns or enable overage to make a probe pass.

The transcription probe accepts `ENDPOINT=transcriptions` with a remote `AUDIO_FILE`.
It requires both a JSON text result (including valid silence) and a usage row for
the target account. It includes total/actual cost and the rate multiplier for
reconciliation. `usage_logs.duration_ms` is execution latency, not audio duration;
compare cost against the known normalized fixture duration and the effective rate.

The activation changes alias billing to the target's price. This is intentional:
the provider already serves the newer model. Existing client IDs remain accepted.
Native media forwarding preserves upstream fields but uses the existing bounded,
nonstreaming media response path. TTS buffers audio until completion, trading first
byte latency for reliable failure handling and settlement.
