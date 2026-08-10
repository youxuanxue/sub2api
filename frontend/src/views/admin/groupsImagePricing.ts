import {
  TK_GROK_IMAGE_ADMIN_PLACEHOLDERS,
  TK_GROK_VIDEO_ADMIN_DESCRIPTION,
  TK_GROK_VIDEO_ADMIN_PLACEHOLDERS,
} from "@/constants/tkVideoOverlayPlaceholders.tk";

export const imagePricingPlatforms = new Set([
  "antigravity",
  "composite",
  "gemini",
  "grok",
  "openai",
]);

export const supportsImagePricingPlatform = (platform: string): boolean =>
  imagePricingPlatforms.has(platform);

export const supportsVideoPricingPlatform = (platform: string): boolean =>
  platform === "grok";

export const imagePricingI18nKey = (_platform: string, key: string): string =>
  `admin.groups.imagePricing.${key}`;

export const videoPricingI18nKey = (key: string): string =>
  `admin.groups.videoPricing.${key}`;

/** Admin Grok video pricing blurb — rates from tk_pricing_overlay.json via export script. */
export const grokVideoPricingDescription = (locale: string): string =>
  locale.startsWith("zh")
    ? TK_GROK_VIDEO_ADMIN_DESCRIPTION.zh
    : TK_GROK_VIDEO_ADMIN_DESCRIPTION.en;

type ImagePricingTierKey = "image_price_1k" | "image_price_2k" | "image_price_4k";
type VideoPricingTierKey =
  | "video_price_480p"
  | "video_price_720p"
  | "video_price_1080p";

const defaultImagePricePlaceholders: Record<
  string,
  Record<ImagePricingTierKey, string>
> = {
  default: {
    image_price_1k: "0.134",
    image_price_2k: "0.201",
    image_price_4k: "0.268",
  },
  grok: {
    image_price_1k: TK_GROK_IMAGE_ADMIN_PLACEHOLDERS.image_price_1k,
    image_price_2k: TK_GROK_IMAGE_ADMIN_PLACEHOLDERS.image_price_2k,
    image_price_4k: TK_GROK_IMAGE_ADMIN_PLACEHOLDERS.image_price_4k,
  },
};

// Video placeholders: 480p/720p from grok-imagine-video; 1080p from video-1.5 (overlay SSOT).
const defaultVideoPricePlaceholders: Record<
  string,
  Record<VideoPricingTierKey, string>
> = {
  grok: {
    video_price_480p: TK_GROK_VIDEO_ADMIN_PLACEHOLDERS.video_price_480p,
    video_price_720p: TK_GROK_VIDEO_ADMIN_PLACEHOLDERS.video_price_720p,
    video_price_1080p: TK_GROK_VIDEO_ADMIN_PLACEHOLDERS.video_price_1080p,
  },
};

export const getImagePricePlaceholder = (
  platform: string,
  tier: ImagePricingTierKey,
): string => {
  const card = defaultImagePricePlaceholders[platform] ?? defaultImagePricePlaceholders.default;
  return card[tier];
};

export const getVideoPricePlaceholder = (
  platform: string,
  tier: VideoPricingTierKey,
): string => {
  const card = defaultVideoPricePlaceholders[platform];
  return card?.[tier] ?? "";
};

export const getDefaultImagePreviewPrice = (
  platform: string,
  tier: ImagePricingTierKey,
): number | null => {
  const placeholder = getImagePricePlaceholder(platform, tier);
  if (placeholder === "") {
    return null;
  }
  const value = Number(placeholder);
  return Number.isFinite(value) ? value : null;
};

export const getDefaultVideoPreviewPrice = (
  platform: string,
  tier: VideoPricingTierKey,
): number | null => {
  const placeholder = getVideoPricePlaceholder(platform, tier);
  if (placeholder === "") {
    return null;
  }
  const value = Number(placeholder);
  return Number.isFinite(value) ? value : null;
};
