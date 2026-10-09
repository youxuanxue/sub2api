/** TokenKey kimi-k3 video-understanding snippets for Quickstart curl/python.
 *  Request-shape contract: docs/approved/multimodal-supply-capability-ssot.md
 *  (customer request shapes). Do not treat Moonshot official docs as TokenKey-only. */

export const KIMI_K3_VIDEO_MODEL = 'kimi-k3'

/** Proven 2026-10-08 on prod Volc Agent Plan: supplier-reachable HTTPS sample. */
export const KIMI_K3_REACHABLE_VIDEO_URL =
  'https://help-static-aliyun-doc.aliyuncs.com/file-manage-files/zh-CN/20241115/cqqkru/1.mp4'

export function isKimiK3VideoGuideModel(model: string): boolean {
  return model === KIMI_K3_VIDEO_MODEL
}

type Language = 'curl' | 'python'

export function kimiK3VideoExamples(
  language: Language,
  root: string,
  apiKey: string,
  hint: string,
): Array<{ path: string; content: string; hint?: string }> {
  const base = root.replace(/\/+$/, '')
  if (language === 'curl') {
    return [
      {
        path: 'cURL (kimi-k3 video · chat + base64)',
        hint,
        content: `VIDEO_B64=$(base64 < clip.mp4 | tr -d '\\n')
curl ${base}/v1/chat/completions \\
  -H "Authorization: Bearer ${apiKey}" \\
  -H "Content-Type: application/json" \\
  -d "{
    \\"model\\": \\"${KIMI_K3_VIDEO_MODEL}\\",
    \\"max_tokens\\": 64,
    \\"messages\\": [{\\"role\\": \\"user\\", \\"content\\": [
      {\\"type\\": \\"text\\", \\"text\\": \\"一句话描述这个视频\\"},
      {\\"type\\": \\"video_url\\", \\"video_url\\": {\\"url\\": \\"data:video/mp4;base64,$VIDEO_B64\\"}}
    ]}]
  }"`,
      },
      {
        path: 'cURL (kimi-k3 video · chat + reachable HTTPS)',
        content: `curl ${base}/v1/chat/completions \\
  -H "Authorization: Bearer ${apiKey}" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": "${KIMI_K3_VIDEO_MODEL}",
    "max_tokens": 64,
    "messages": [{"role": "user", "content": [
      {"type": "text", "text": "一句话描述这个视频"},
      {"type": "video_url", "video_url": {"url": "${KIMI_K3_REACHABLE_VIDEO_URL}"}}
    ]}]
  }'`,
      },
      {
        path: 'cURL (kimi-k3 video · responses + reachable HTTPS)',
        content: `curl ${base}/v1/responses \\
  -H "Authorization: Bearer ${apiKey}" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": "${KIMI_K3_VIDEO_MODEL}",
    "input": [{"role": "user", "content": [
      {"type": "input_text", "text": "一句话描述这个视频"},
      {"type": "input_video", "video_url": "${KIMI_K3_REACHABLE_VIDEO_URL}"}
    ]}]
  }'`,
      },
    ]
  }
  return [
    {
      path: 'kimi_k3_video_chat.py',
      hint,
      content: `from pathlib import Path
import base64
from openai import OpenAI

client = OpenAI(api_key=${JSON.stringify(apiKey)}, base_url=${JSON.stringify(base)})
video_b64 = base64.b64encode(Path("clip.mp4").read_bytes()).decode()
resp = client.chat.completions.create(
    model=${JSON.stringify(KIMI_K3_VIDEO_MODEL)},
    max_tokens=64,
    messages=[{
        "role": "user",
        "content": [
            {"type": "text", "text": "一句话描述这个视频"},
            {"type": "video_url", "video_url": {"url": f"data:video/mp4;base64,{video_b64}"}},
        ],
    }],
)
print(resp.choices[0].message.content)`,
    },
    {
      path: 'kimi_k3_video_responses.py',
      content: `from openai import OpenAI

client = OpenAI(api_key=${JSON.stringify(apiKey)}, base_url=${JSON.stringify(base)})
resp = client.responses.create(
    model=${JSON.stringify(KIMI_K3_VIDEO_MODEL)},
    input=[{
        "role": "user",
        "content": [
            {"type": "input_text", "text": "一句话描述这个视频"},
            {"type": "input_video", "video_url": ${JSON.stringify(KIMI_K3_REACHABLE_VIDEO_URL)}},
        ],
    }],
)
print(resp)`,
    },
  ]
}
