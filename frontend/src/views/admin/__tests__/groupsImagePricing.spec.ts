import { describe, expect, it } from "vitest";

import {
  TK_GROK_IMAGE_ADMIN_PLACEHOLDERS,
  TK_GROK_VIDEO_ADMIN_DESCRIPTION,
  TK_GROK_VIDEO_ADMIN_PLACEHOLDERS,
} from "@/constants/tkVideoOverlayPlaceholders.tk";
import {
  getDefaultImagePreviewPrice,
  getDefaultVideoPreviewPrice,
  getImagePricePlaceholder,
  getVideoPricePlaceholder,
  grokVideoPricingDescription,
  imagePricingPlatforms,
  imagePricingI18nKey,
  supportsImagePricingPlatform,
  supportsVideoPricingPlatform,
  videoPricingI18nKey,
} from "../groupsImagePricing";

describe("groups image pricing platform support", () => {
  it("includes Grok image groups", () => {
    expect(supportsImagePricingPlatform("grok")).toBe(true);
    expect(imagePricingPlatforms.has("grok")).toBe(true);
  });

  it("enables video pricing controls for Grok only", () => {
    expect(supportsVideoPricingPlatform("grok")).toBe(true);
    expect(supportsVideoPricingPlatform("openai")).toBe(false);
  });

  it("keeps non-media group platforms out of the image pricing controls", () => {
    expect(supportsImagePricingPlatform("anthropic")).toBe(false);
  });

  it("keeps image and video pricing copy separate", () => {
    expect(imagePricingI18nKey("grok", "title")).toBe(
      "admin.groups.imagePricing.title",
    );
    expect(videoPricingI18nKey("title")).toBe("admin.groups.videoPricing.title");
  });

  it("uses Grok media defaults instead of generic image fallback placeholders", () => {
    expect(getImagePricePlaceholder("grok", "image_price_1k")).toBe(
      TK_GROK_IMAGE_ADMIN_PLACEHOLDERS.image_price_1k,
    );
    expect(getImagePricePlaceholder("grok", "image_price_2k")).toBe(
      TK_GROK_IMAGE_ADMIN_PLACEHOLDERS.image_price_2k,
    );
    expect(getVideoPricePlaceholder("grok", "video_price_480p")).toBe(
      TK_GROK_VIDEO_ADMIN_PLACEHOLDERS.video_price_480p,
    );
    expect(getVideoPricePlaceholder("grok", "video_price_720p")).toBe(
      TK_GROK_VIDEO_ADMIN_PLACEHOLDERS.video_price_720p,
    );
    expect(getVideoPricePlaceholder("grok", "video_price_1080p")).toBe(
      TK_GROK_VIDEO_ADMIN_PLACEHOLDERS.video_price_1080p,
    );
  });

  it("derives Grok video admin copy from overlay export", () => {
    expect(grokVideoPricingDescription("zh-CN")).toBe(
      TK_GROK_VIDEO_ADMIN_DESCRIPTION.zh,
    );
    expect(grokVideoPricingDescription("en")).toBe(
      TK_GROK_VIDEO_ADMIN_DESCRIPTION.en,
    );
  });

  it("keeps non-Grok image placeholders on the generic image card", () => {
    expect(getImagePricePlaceholder("openai", "image_price_1k")).toBe("0.134");
    expect(getDefaultImagePreviewPrice("openai", "image_price_2k")).toBe(0.201);
    expect(getDefaultVideoPreviewPrice("openai", "video_price_480p")).toBeNull();
  });
});
