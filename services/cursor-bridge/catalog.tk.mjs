import { createHash } from 'node:crypto';

function signature(params) {
  if (!Array.isArray(params) || params.some(p => typeof p?.id !== 'string' || typeof p?.value !== 'string')) return null;
  return JSON.stringify(params.map(p => [p.id, p.value]).sort((a, b) => a[0].localeCompare(b[0])));
}

export function createCatalogValidator(cursor, { ttlMs = 300_000, maxEntries = 64, now = Date.now } = {}) {
  const cache = new Map();
  return async (body, apiKey) => {
    const selected = body?.cursor_model;
    if (!selected || selected.id !== body.model || ['default', 'auto'].includes(selected.id)) {
      throw Object.assign(new Error('A fixed Cursor catalog model is required'), { status: 400 });
    }
    const key = createHash('sha256').update(apiKey).digest('hex');
    let entry = cache.get(key);
    if (!entry || entry.expires <= now()) {
      const models = await cursor.models.list({ apiKey });
      if (cache.size >= maxEntries) cache.delete(cache.keys().next().value);
      entry = { models, expires: now() + ttlMs };
      cache.set(key, entry);
    }
    const model = entry.models.find(model => model.id === selected.id);
    const params = signature(selected.params);
    if (!model || params === null || !model.variants?.some(variant => signature(variant.params) === params)) {
      throw Object.assign(new Error('Cursor model or variant is not available for this account'), { status: 400 });
    }
  };
}
