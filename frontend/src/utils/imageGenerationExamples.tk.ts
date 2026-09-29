import { buildImageRequest, type ImageGenerationOptions, type ImageGenerationPlan } from './imageGeneration.tk'

const shellQuote = (value: string) => `'${value.replace(/'/g, "'\\''")}'`

/** Snippets and Studio serialize the exact same request. */
export function imageGenerationExample(language: 'curl' | 'python', root: string, apiKey: string, model: string, prompt: string, plan: ImageGenerationPlan, options: ImageGenerationOptions) {
  const request = buildImageRequest(model, prompt, plan, options)
  const url = root.replace(/\/+$/, '') + request.endpoint
  const body = JSON.stringify(request.body, null, 2)
  if (language === 'curl') return {
    path: 'terminal',
    content: `curl ${shellQuote(url)} \\\n  -H ${shellQuote(`Authorization: Bearer ${apiKey}`)} \\\n  -H 'Content-Type: application/json' \\\n  --data-binary ${shellQuote(body)} \\\n  --output response.json`,
  }
  return {
    path: 'generate_image.py',
    content: `# pip install requests
import base64
import json
import re
from pathlib import Path

import requests

payload = json.loads(${JSON.stringify(JSON.stringify(request.body))})
response = requests.post(
    ${JSON.stringify(url)},
    headers={"Authorization": ${JSON.stringify(`Bearer ${apiKey}`)}},
    json=payload,
    timeout=180,
)
response.raise_for_status()
result = response.json()
Path("response.json").write_text(json.dumps(result, ensure_ascii=False), encoding="utf-8")

images = []
for item in result.get("data", []):
    if item.get("b64_json"):
        images.append("data:image/png;base64," + item["b64_json"])
    elif item.get("url"):
        images.append(item["url"])

def collect(content):
    if isinstance(content, str):
        images.extend(re.findall(r"!\\[[^\\]]*\\]\\(([^)]+)\\)", content))
    elif isinstance(content, list):
        for part in content:
            collect(part)
    elif isinstance(content, dict):
        image = content.get("image_url")
        if image:
            images.append(image.get("url") if isinstance(image, dict) else image)
        inline = content.get("inlineData") or content.get("inline_data")
        if inline and inline.get("data"):
            mime = inline.get("mimeType") or inline.get("mime_type") or "image/png"
            images.append("data:" + mime + ";base64," + inline["data"])
        source = content.get("source", {})
        if content.get("type") == "image" and source.get("type") == "base64":
            images.append("data:" + source.get("media_type", "image/png") + ";base64," + source["data"])
        if content.get("text"):
            collect(content["text"])

for choice in result.get("choices", []):
    collect(choice.get("message", {}).get("content"))
for candidate in result.get("candidates", []):
    collect(candidate.get("content", {}).get("parts", []))

if not images:
    raise RuntimeError("No image returned; inspect response.json")
for index, src in enumerate(dict.fromkeys(images), start=1):
    if src.startswith("data:image/"):
        header, encoded = src.split(",", 1)
        mime = header.split(";", 1)[0][5:]
        data = base64.b64decode(encoded)
    else:
        # Signed image URLs do not need the TokenKey credential.
        download = requests.get(src, timeout=180)
        download.raise_for_status()
        mime = download.headers.get("Content-Type", "").split(";", 1)[0]
        data = download.content
    extension = {"image/png": "png", "image/jpeg": "jpg", "image/webp": "webp"}.get(mime, "bin")
    filename = Path(f"image-{index}.{extension}")
    filename.write_bytes(data)
    print(filename)
`,
  }
}
