# BigModel GLM pricing capture — 2026-07-09

Source: <https://bigmodel.cn/pricing>

Important boundary:

- This source is used for GLM pricing only.
- TokenKey still serves manifest-listed GLM chat models through Alibaba
  DashScope via the configured Qwen pool (`channel_type=17`). The live account
  membership is runtime DB/admin config, not pricing evidence.
- Do not infer a BigModel/Zhipu direct serving path from this pricing source.

The page is JS-rendered. On 2026-07-09 the HTML loaded:

- `https://static.bigmodel.cn/wd-paas-front/js/runtime.8932dc5e.js`
- `https://static.bigmodel.cn/wd-paas-front/js/app.3b0fabe9.js`
- lazy route `Pricing.1faed962.js`

The GLM pricing rows below are embedded in the current page bundle under the
`pricing_page.latestModel` text model table. Unit is RMB per million tokens.
TokenKey overlay/fallback stores the pre-tax USD-per-token value as:

`CNY per MTok / 6.7 / 1_000_000`

Billing/catalog resolution then applies the normal `1.06` zhipu base-tax
multiplier, so final customer-facing charge/display is:

`CNY per MTok / 6.7 / 1_000_000 * 1.06`

## Current Text Rows

| Model | Input length | Output length | Input CNY/MTok | Output CNY/MTok | Cache-hit CNY/MTok |
| --- | --- | --- | ---: | ---: | ---: |
| GLM-5.2 | 1M | any | 8 | 28 | 2 |
| GLM-5.1 | [0, 32K) | any | 6 | 24 | 1.3 |
| GLM-5.1 | [32K, +) | any | 8 | 28 | 2 |
| GLM-5-Turbo | [0, 32K) | any | 5 | 22 | 1.2 |
| GLM-5-Turbo | [32K, +) | any | 7 | 26 | 1.8 |
| GLM-5 | [0, 32K) | any | 4 | 18 | 1 |
| GLM-5 | [32K, +) | any | 6 | 22 | 1.5 |
| GLM-4.7 | [0, 32K) | [0, 0.2K) | 2 | 8 | 0.4 |
| GLM-4.7 | [0, 32K) | [0.2K, +) | 3 | 14 | 0.6 |
| GLM-4.7 | [32K, 200K) | any | 4 | 16 | 0.8 |
| GLM-4.5-Air | [0, 32K) | [0, 0.2K) | 0.8 | 2 | 0.16 |
| GLM-4.5-Air | [0, 32K) | [0.2K, +) | 0.8 | 6 | 0.16 |
| GLM-4.5-Air | [32K, 128K) | any | 1.2 | 8 | 0.24 |
| GLM-4.7-FlashX | 200K | any | 0.5 | 3 | 0.1 |
| GLM-4.7-Flash | 200K | any | free | free | free |

## Modeling Notes

- TokenKey intervals key by input tokens only. For GLM-4.7 and GLM-4.5-Air,
  the official table has an additional short-output discount in the 0-32K input
  band. The overlay uses the higher reachable output>0.2K row in that input band
  to avoid under-billing requests that produce longer output.
- The current BigModel pricing page does not publish separate text-token rows
  for legacy `glm-4.6` or `glm-4.5`. TokenKey keeps legacy requests priced to
  the current GLM-4.7 paid ladder rather than retaining the old USD-source rate.
- The current BigModel pricing page does not publish `glm-4.5-x` or
  `glm-4.5-airx`; TokenKey does not retain the old Z.AI USD-source prices for
  those direct-only legacy names.
- `glm-4.7-flash` is free on the official page and intentionally not added as a
  positive-price overlay entry or public manifest row.
