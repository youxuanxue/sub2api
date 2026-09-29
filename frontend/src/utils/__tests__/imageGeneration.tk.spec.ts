import { describe, expect, it } from 'vitest'
import { mkdtempSync, writeFileSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { spawnSync } from 'node:child_process'
import { buildImageRequest, extractGeneratedImages, imageGenerationPlan, imageStudioPath, normalizeImageOptions } from '../imageGeneration.tk'
import { imageGenerationExample } from '../imageGenerationExamples.tk'
import type { ImageGenerationCapability } from '@/api/api-key-capabilities'

const gpt: ImageGenerationCapability = { endpoint: '/v1/images/generations', aspect_ratios: ['1:1', '16:9'], counts: [1, 2], input_image: false, soft_aspect_ratio: true }
const gemini: ImageGenerationCapability = { endpoint: '/v1/chat/completions', aspect_ratios: ['1:1', '4:3'], counts: [1], input_image: true, soft_aspect_ratio: false }

describe('shared image generation contract', () => {
  it('normalizes switch, stale restore and unknown capabilities without inventing controls', () => {
    const plan = imageGenerationPlan('gpt-image-1', [gpt])
    expect(plan.exactCanvas).toBe(true)
    expect(plan.softAspectRatio).toBe(false)
    expect(plan.ratioField).toBe(false)
    expect(plan.sizes.map(s => s.ratio)).toEqual(['1:1', '16:9'])
    expect(normalizeImageOptions(plan, { ratio: '4:3', n: 4, inputImage: 'data:image/png;base64,AA==' })).toEqual({ ratio: '1:1', n: 1, inputImage: '' })
    expect(buildImageRequest('gpt-image-1', 'draw', plan, { ratio: '16:9', n: 2 }).body).toEqual({ model: 'gpt-image-1', prompt: 'draw', size: '2048x1152', n: 2 })
    expect(normalizeImageOptions(imageGenerationPlan('gpt-image-1', [{ ...gpt, aspect_ratios: ['1:1', '3:2'] }]), { ratio: '1536x1024' }).ratio).toBe('3:2')
    const unknown = imageGenerationPlan('future-image-model')
    expect(buildImageRequest('future-image-model', 'draw', unknown, { ratio: '21:9', n: 4 }).body).toEqual({ model: 'future-image-model', prompt: 'draw' })
    const preview = imageGenerationPlan('gpt-image-2')
    expect(preview.exactCanvas).toBe(true)
    expect(preview.sizes.map(s => s.value)).toEqual(['1024x1024', '1536x1024', '1024x1536', '2048x1152', '1152x2048'])
    expect(buildImageRequest('gpt-image-2', 'draw', preview, { ratio: '1:1' }).body).toEqual({ model: 'gpt-image-2', prompt: 'draw', size: '1024x1024' })
    const seedream = imageGenerationPlan('seedream-4-0-250828', [{ ...gpt, soft_aspect_ratio: false, aspect_ratios: [] }])
    expect(buildImageRequest('seedream-4-0-250828', 'draw', seedream, { ratio: '16:9' }).body).toEqual({ model: 'seedream-4-0-250828', prompt: 'draw', size: '2048x1152' })
  })

  it('uses complete profiles, preserves compatible ratios and carries Gemini input on chat', () => {
    const profile = imageGenerationPlan('nano-2', [gemini, { ...gemini, aspect_ratios: ['16:9'], input_image: false }])
    expect(profile.sizes.map(s => s.ratio)).toEqual(gemini.aspect_ratios)
    const body = buildImageRequest('nano-2', 'draw', profile, { ratio: '4:3', inputImage: 'data:image/png;base64,AA==' }).body
    expect(body).toEqual({ model: 'nano-2', stream: false, messages: [{ role: 'user', content: [{ type: 'text', text: 'draw' }, { type: 'image_url', image_url: { url: 'data:image/png;base64,AA==' } }] }], extra_body: { google: { image_config: { aspect_ratio: '4:3' } } } })
    expect(imageStudioPath('nano-2', { ratio: '4:3', n: 1 }, 42)).toBe('/studio?mode=image&model=nano-2&ratio=4%3A3&key=42')
  })

  it('uses the admitted endpoint when the preferred ingress is absent', () => {
    const plan = imageGenerationPlan('nano-2', [{ ...gemini, endpoint: '/v1beta/models/nano-2:generateContent' }])
    expect(buildImageRequest('nano-2', 'draw', plan, { ratio: '4:3' })).toEqual({ endpoint: '/v1beta/models/nano-2:generateContent', body: { contents: [{ role: 'user', parts: [{ text: 'draw' }] }], generationConfig: { responseModalities: ['TEXT', 'IMAGE'], imageConfig: { aspectRatio: '4:3' } } } })
    expect(extractGeneratedImages({ candidates: [{ content: { parts: [{ inlineData: { mimeType: 'image/png', data: 'AA==' } }] } }] }, plan)).toEqual([{ src: 'data:image/png;base64,AA==' }])
  })

  it('executes Python examples, serializes the same request and saves URL, base64 and chat/native images', () => {
    const dir = mkdtempSync(join(tmpdir(), 'tk-image-example-'))
    try {
      writeFileSync(join(dir, 'requests.py'), `import json
class Response:
    content = b'fixture-image'
    headers = {'Content-Type': 'image/png'}
    def raise_for_status(self): pass
    def json(self):
        return {'data': [{'b64_json': 'Zml4dHVyZS1pbWFnZQ=='}, {'url': 'https://images.example.test/a.png'}], 'choices': [{'message': {'content': '![image](data:image/png;base64,Zml4dHVyZS1jaGF0)'}}], 'candidates': [{'content': {'parts': [{'inlineData': {'mimeType': 'image/png', 'data': 'Zml4dHVyZS1uYXRpdmU='}}]}}]}
def post(url, headers, json, timeout):
    from pathlib import Path
    import json as codec
    Path('request.json').write_text(codec.dumps({'url': url, 'body': json}))
    return Response()
def get(url, timeout): return Response()
`)
      const plan = imageGenerationPlan('gpt-image-1', [gpt])
      const options = normalizeImageOptions(plan, { ratio: '16:9' })
      const prompt = `Artist's cat; $(touch should-not-exist) \"你好\"\nA second line`
      const example = imageGenerationExample('python', 'https://gateway.test', 'placeholder', 'gpt-image-1', prompt, plan, options)
      writeFileSync(join(dir, 'example.py'), example.content)
      const result = spawnSync('python3', ['example.py'], { cwd: dir, encoding: 'utf8' })
      expect(result.stderr).toBe('')
      expect(result.status).toBe(0)
      expect(JSON.parse(readFileSync(join(dir, 'request.json'), 'utf8'))).toEqual({ url: 'https://gateway.test/v1/images/generations', body: buildImageRequest('gpt-image-1', prompt, plan, options).body })
      expect(readFileSync(join(dir, 'image-1.png'), 'utf8')).toBe('fixture-image')
      expect(readFileSync(join(dir, 'image-2.png'), 'utf8')).toBe('fixture-image')
      expect(readFileSync(join(dir, 'image-3.png'), 'utf8')).toBe('fixture-chat')
      expect(readFileSync(join(dir, 'image-4.png'), 'utf8')).toBe('fixture-native')
    } finally { rmSync(dir, { recursive: true, force: true }) }
  })
})
