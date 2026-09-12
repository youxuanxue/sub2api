/** Return destinations contain no credentials and must remain on this origin. */
export function safeInternalRedirect(value: unknown, fallback = '/quickstart'): string {
  if (typeof value !== 'string' || !value.startsWith('/') || value.startsWith('//') || (value.includes('\\') || [...value].some(char => char.charCodeAt(0) <= 32))) return fallback
  return value
}

export function quickstartReturnPath(client: string, protocol: string, transport: string): string {
  const query = new URLSearchParams({ client })
  if (client === 'qwen-code') query.set('protocol', protocol)
  if (client === 'codex-cli') query.set('transport', transport)
  return `/quickstart?${query}`
}
