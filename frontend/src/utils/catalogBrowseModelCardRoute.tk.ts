/** Browse-tab model card: authed users integrate; guests inspect pricing first. */
export function catalogBrowseModelCardRoute(
  modelId: string,
  options: { isAuthenticated: boolean },
): { path: string; query: Record<string, string> } {
  if (options.isAuthenticated) {
    return { path: '/quickstart', query: { model: modelId } }
  }
  return { path: '/models', query: { view: 'pricing', model: modelId } }
}
