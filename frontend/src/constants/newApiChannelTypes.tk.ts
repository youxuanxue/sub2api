/** new-api channel_type for Google Vertex AI (imagen / veo / Vertex Gemini bridge). */
export const NEW_API_CHANNEL_TYPE_VERTEX_AI = 41

export function isNewApiVertexServiceAccountChannelType(channelType: number): boolean {
  return channelType === NEW_API_CHANNEL_TYPE_VERTEX_AI
}
