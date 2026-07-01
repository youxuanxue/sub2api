import { ref } from 'vue'

/**
 * Shared video-generation toggles for VideoStudio and BakeOff (SSOT for submit
 * params that apply across compared models).
 */
export function useStudioVideoSubmitOptions() {
  /** Default on — Veo/Seedance upstreams generate audio unless explicitly disabled. */
  const generateAudio = ref(true)
  return { generateAudio }
}
