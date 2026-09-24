/**
 * Maps public pricing catalog `vendor` slugs to ModelIcon keys.
 * Vendor strings come from LiteLLM/upstream and vary (OpenAI, vertex_ai-language-models, …).
 */
export function resolveCatalogVendorIconKey(vendor: string): string | null {
  const raw = normalizeCatalogVendorSlug(vendor).toLowerCase()
  if (!raw || raw === 'unknown') return null

  if (raw === 'openai' || raw.startsWith('openai-')) return 'openai'
  if (raw.includes('anthropic') || raw.includes('claude')) return 'claude'
  if (
    raw === 'google'
    || raw === 'gemini'
    || raw === 'antigravity'
    || raw === 'vertex_ai'
    || raw.startsWith('vertex_ai')
    || raw.includes('vertex')
  ) {
    return 'gemini'
  }
  if (raw === 'xai' || raw.includes('grok')) return 'xai'
  if (raw === 'volcengine' || raw.includes('doubao') || raw.includes('bytedance')) return 'doubao'
  if (raw.includes('qwen') || raw.includes('alibaba') || raw.includes('dashscope')) return 'qwen'
  if (raw.includes('deepseek')) return 'deepseek'
  if (raw.includes('zhipu') || raw.includes('glm') || raw.includes('chatglm')) return 'zhipu'
  if (raw.includes('moonshot') || raw.includes('kimi')) return 'moonshot'
  if (raw.includes('mistral') || raw.includes('mixtral') || raw.includes('codestral')) return 'mistral'
  if (raw.includes('meta') || raw.includes('llama')) return 'meta'
  if (raw.includes('cohere') || raw.includes('command-r')) return 'cohere'
  if (raw.includes('minimax') || raw.includes('abab')) return 'minimax'
  if (raw.includes('ernie') || raw.includes('wenxin') || raw.includes('baidu')) return 'wenxin'
  if (raw.includes('spark') || raw.includes('iflytek')) return 'spark'
  if (raw.includes('hunyuan') || raw.includes('tencent')) return 'hunyuan'
  if (raw.includes('midjourney') || raw.startsWith('mj')) return 'midjourney'
  if (raw.includes('perplexity') || raw.includes('pplx')) return 'perplexity'
  if (raw.includes('cloudflare') || raw.includes('@cf/')) return 'cloudflare'
  if (raw.includes('jina')) return 'jina'
  if (raw.includes('openrouter')) return 'openrouter'
  if (raw.includes('suno')) return 'suno'
  if (raw.includes('ollama')) return 'ollama'
  if (raw.includes('360')) return 'ai360'
  if (raw.includes('yi-') || raw === 'yi') return 'yi'

  return null
}

/** Shared public vendor identity for grouping, labels and icons. */
export function normalizeCatalogVendorSlug(vendor: string): string {
  const trimmed = vendor.trim() || 'Unknown'
  const normalized = trimmed.toLowerCase()
  if (
    ['google', 'gemini', 'antigravity', 'vertex', 'vertexai', 'vertex_ai'].includes(normalized)
    || normalized.startsWith('vertex_ai-')
  ) return 'google'
  if (['wenxin', 'qianfan', 'baidu'].includes(normalized)) return 'baidu'
  return trimmed
}

export function formatCatalogVendorLabel(vendor: string): string {
  const slug = normalizeCatalogVendorSlug(vendor)
  const labels: Record<string, string> = {
    openai: 'OpenAI',
    cursor: 'Composer',
    anthropic: 'Anthropic',
    volcengine: 'VolcEngine',
    xai: 'xAI',
    google: 'Google',
    qwen: 'Qwen',
    deepseek: 'DeepSeek',
    zhipu: 'Zhipu',
    moonshot: 'Moonshot',
    mistral: 'Mistral',
    meta: 'Meta',
    cohere: 'Cohere',
    minimax: 'MiniMax',
    hunyuan: 'Hunyuan',
    midjourney: 'Midjourney',
    perplexity: 'Perplexity',
    baidu: 'Baidu',
  }
  const key = slug.toLowerCase()
  if (labels[key]) return labels[key]
  if (key.includes('openai')) return 'OpenAI'
  return slug
}
