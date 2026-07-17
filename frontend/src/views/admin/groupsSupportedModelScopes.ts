import { PLATFORM_ANTIGRAVITY } from '@/constants/gatewayPlatforms'

export const normalizeSupportedModelScopesForPlatform = (
  platform: string,
  scopes: string[] | undefined,
): string[] => {
  if (platform !== PLATFORM_ANTIGRAVITY) return [];
  return scopes ?? [];
};
