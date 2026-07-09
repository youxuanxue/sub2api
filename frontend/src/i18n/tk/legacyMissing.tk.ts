// TokenKey-only locale overlay for keys that existed in the pre-upstream
// single-file locale baseline but are not present in upstream's split locale
// modules. Kept out of locales/{en,zh}/** so the upstream directory shape stays
// merge-safe; i18n/index.ts deep-merges this after loading the base locale.

type LocaleOverlay = Record<string, unknown>

const en: LocaleOverlay = {
  "pricing": {
    "title": "Model Pricing",
    "subtitle": "Pay-as-you-go · USD per 1K tokens",
    "description": "Prices are per 1,000 tokens, in USD. Cache Read / Write columns apply only to models that bill cached tokens separately. Capabilities reflect upstream-declared features (vision, tools, …).",
    "nav": {
      "aria": "Leave pricing page",
      "home": "Home",
      "console": "Console",
      "consoleTitleAuthed": "Go to your dashboard",
      "consoleTitleGuest": "Sign in to open the console"
    },
    "columns": {
      "model": "Model",
      "vendor": "Vendor",
      "input": "Input",
      "output": "Output",
      "cacheRead": "Cache Read",
      "cacheWrite": "Cache Write",
      "contextWindow": "Context window",
      "maxOutput": "Max output",
      "capabilities": "Capabilities"
    },
    "tableHint": "Swipe horizontally or scroll below to see all columns. Model names wrap in the left column.",
    "export": {
      "button": "Export CSV",
      "success": "Pricing exported",
      "empty": "Public catalog is empty — nothing to export"
    },
    "tieredBadge": "Tiered ×{n}",
    "footer": {
      "total": "{count} models listed",
      "filtered": "Showing {shown} of {total} models"
    },
    "perThousandTokens": "/ 1K tokens",
    "thinkingOutput": "Thinking",
    "contextTokens": "{count} tokens",
    "updatedAt": "Last updated {time}",
    "empty": {
      "title": "Pricing catalog is being prepared",
      "hint": "No model pricing data is available yet. Please check back later or contact the administrator."
    },
    "errorTitle": "Failed to load pricing",
    "errorHint": "Please refresh the page or try again later.",
    "retry": "Retry",
    "search": {
      "placeholder": "Search by model name…",
      "modeLabel": "Match mode",
      "modeFuzzy": "Contains",
      "modeExact": "Exact name",
      "resultCount": "{count} models",
      "noMatches": "No models match your search. Try fuzzy mode or a shorter query."
    },
    "filters": {
      "apiKey": "API Key",
      "keyPlaceholder": "All keys",
      "group": "Group",
      "publicCatalog": "All groups",
      "groupExclusiveOption": "{group} (exclusive)",
      "search": "Search",
      "activePublic": "Viewing all groups",
      "activeGroup": "Viewing {group} group catalog",
      "activeKeyGroup": "Viewing {key} · {group}"
    },
    "ctaBonus": "Register — get {amount} trial credit",
    "ctaBonusHint": "Shown when signup bonus is enabled for this site.",
    "perRequest": "/ request",
    "perImage": "/ image",
    "perSecond": "/ second",
    "videoClipExample": "5s {five} · 10s {ten}",
    "modality": {
      "all": "All",
      "text": "Text",
      "image": "Image",
      "video": "Video"
    },
    "my": {
      "tabMy": "Group Catalog",
      "tabPublic": "All groups",
      "title": "Group Model Catalog",
      "subtitle": "Models available to the group of your selected API key, at official pricing",
      "description": "Shows the models available to the selected group and their official pricing, per 1,000 tokens. Switch keys to see another group, or compare other accessible groups.",
      "pickerKey": "Current key:",
      "pickerCompare": "Compare group:",
      "compareDefault": "Keep current",
      "noKeyHint": "You have no active API keys yet — create one in the console first.",
      "columns": {
        "input": "Input (official price)",
        "output": "Output (official price)",
        "authorizedGroups": "Authorized Groups"
      },
      "authorizedGroups": {
        "groupHint": "{group} can serve this model",
        "exclusive": "exclusive",
        "quickstart": "Quick start"
      },
      "empty": {
        "noAccess": {
          "title": "No accessible group",
          "hint": "You have no group you can use yet. Contact the administrator or pick a plan in the subscription page."
        },
        "noModels": {
          "title": "This group has no models yet",
          "hint": "The administrator may still be configuring channels. Check back later or compare another group."
        }
      },
      "exploreBanner": {
        "message": "Viewing {group} catalog",
        "cta": "Create key in {group}"
      },
      "billingMode": {
        "per_request": "per call",
        "image": "per image"
      }
    }
  },
  "studio": {
    "title": "Studio",
    "subtitle": "Try everything your key can do — chat, images, and video, all in one place.",
    "balance": "Balance",
    "apiKey": "API Key",
    "pickKeyPlaceholder": "Select an API key…",
    "loadingModels": "Loading models…",
    "loadFailed": "Could not load Studio.",
    "noApiKey": "No active API key found. Create one under API Keys.",
    "manageKeys": "Manage API keys",
    "defaultGroup": "Default group",
    "modeChat": "Chat",
    "modeImage": "Image",
    "modeVideo": "Video",
    "modeBakeoff": "Bake-Off",
    "clear": "Clear",
    "topUp": "Top up",
    "viewPricing": "See pricing",
    "via": "via {vendor}",
    "keyNoModality": "no models for this mode",
    "saveReminderTitle": "Download your results now",
    "saveReminder": "Stored only in this browser (~7 days). Clearing cache, private mode, or switching devices removes previews — download files to keep them.",
    "universalKeyBadge": "Universal key",
    "playback": {
      "inlineLocal": "Local cache · preview survives reload (~7 days)",
      "upstreamCorsOk": "Upstream URL · CORS fetch OK · download anyway",
      "upstreamCorsBlocked": "Upstream URL · CORS blocks cache · no preview after reload · download now",
      "expired": "Preview gone · prompt only kept",
      "unknown": "Storage unknown · download now",
      "label": "Preview source"
    },
    "needsApikeyAccount": "Needs an API-key account",
    "badge": {
      "draft": "Draft",
      "standard": "Standard",
      "ultra": "Ultra",
      "fast": "Fast",
      "cinematic": "Cinematic"
    },
    "advanced": {
      "toggle": "Advanced",
      "negativePrompt": "Avoid",
      "negativePromptHint": "Describe what to keep out (optional)",
      "seed": "Seed",
      "seedHint": "Leave blank for random",
      "firstFrame": "First frame image (URL)",
      "firstFrameHint": "Paste an image URL to use as the first frame (optional)"
    },
    "bakeoff": {
      "hint": "One prompt, several models side by side — each with its real price and speed.",
      "needTwo": "Need at least two priced models in this group to compare.",
      "promptPlaceholder": "Describe what to generate across models…",
      "pickModels": "Compare:",
      "run": "Generate across {count}",
      "running": "Generating…",
      "regenerate": "Regenerate",
      "regenerateChanged": "Generate with new prompt",
      "clearResults": "Clear current view",
      "historyTitle": "Past comparisons",
      "maxModelsHint": "Compare up to {max} models at once — deselect one to add another.",
      "totalCost": "Total ≈ {cost}",
      "cannotAfford": "Balance too low for this run.",
      "generating": "Generating…",
      "failed": "Failed",
      "panelError": "Generation failed"
    },
    "cost": {
      "thisGeneration": "This generation",
      "thisVideo": "This video",
      "estimate": "Estimate",
      "balance": "Balance",
      "afterGeneration": "After"
    },
    "errors": {
      "insufficient_balance": "Insufficient balance — top up to generate.",
      "permission": "This group is not enabled for this generation type.",
      "unpriced": "This model is not available for generation right now.",
      "rate_limited": "Too many requests — slow down and try again shortly.",
      "unauthorized": "Your API key is invalid or expired.",
      "unsupported_model": "This key/group cannot serve that model — switch keys or deselect it.",
      "generic": "Generation failed. Please try again."
    },
    "image": {
      "modelLabel": "Model",
      "modelEmpty": "No image models are available for this group yet.",
      "samplePrompt": "A neon Tokyo alley at night, cinematic, shallow depth of field",
      "perImageUnit": " /image",
      "promptPlaceholder": "Describe the image…",
      "aspectLabel": "Aspect ratio",
      "billedAs": "Billed as {tier} · ×{mult}",
      "billedFlat": "Flat per-image price (one image per run)",
      "count": "Count",
      "resultsTitle": "Results",
      "emptyHint": "Pick a tier and generate — results appear here.",
      "download": "Download",
      "downloadAll": "Download all",
      "usePrompt": "Use prompt",
      "useAsInput": "Use as input",
      "enlargeHint": "Click to enlarge",
      "expiredReload": "Local preview expired — download to keep, or reuse the prompt (billed again).",
      "close": "Close",
      "inputImageLabel": "Input image (image-to-image)",
      "inputUpload": "Upload image",
      "inputRemove": "Remove",
      "inputHint": "Upload — or pick a result below — as the base image, then describe your edit above (Gemini-native image models only).",
      "reversePrompt": "Generate prompt from image",
      "reversing": "Reading image…",
      "reverseEmpty": "Could not derive a prompt from the image.",
      "revisedPromptHint": "Prompt the model actually used: {text}",
      "generate": "Generate · {cost}",
      "generateTopUp": "Generate · {cost} — top up",
      "generating": "Generating…",
      "noResult": "The gateway returned no image data.",
      "formula": "{base} × {tier} ×{mult} × {n}",
      "formulaFlat": "{base} × {n}"
    },
    "video": {
      "modelLabel": "Model",
      "modelEmpty": "No video models are available for this group yet.",
      "modelEmptySwitchKey": "Video models (Veo, Seedance, Grok, etc.) live on platform-specific groups. Switch to an API key that serves video above.",
      "modelEmptyAllKeys": "None of your key groups expose video models yet. See pricing for supported groups or ask an admin.",
      "samplePrompt": "A neon Tokyo alley, slow push-in, rain, reflections, cinematic",
      "perSecondUnit": " /s",
      "promptPlaceholder": "Describe the video…",
      "duration": "Duration",
      "aspect": "Aspect ratio",
      "aspectAuto": "Auto",
      "emptyHint": "Describe a video and generate — submit and walk away; it plays here when ready.",
      "statusProcessing": "Generating",
      "statusSucceeded": "Ready",
      "statusRefunded": "Failed — refunded {cost}",
      "stepSubmitted": "Submitted",
      "stepGenerating": "Generating",
      "stepReady": "Ready",
      "reserved": "{cost} reserved · refunded if it fails",
      "notifyMe": "Notify me when done",
      "usuallyTakes": "Usually 30–90s",
      "noUrlHint": "Task succeeded. Video results are not retained long-term, so it cannot be played here — generate again to view it.",
      "expiredReload": "Link expired — only the prompt is kept. Reuse it to generate again.",
      "download": "Download",
      "failedRefunded": "Generation failed — {cost} refunded. Try a shorter clip or another tier.",
      "techDetails": "Technical details",
      "refundLine": "Failed videos are refunded in full",
      "generate": "Generate video · {cost}",
      "generateTopUp": "Generate · {cost} — top up",
      "submitting": "Submitting…",
      "formula": "{rate}/s × {seconds}s",
      "noTaskId": "Submit returned no task id.",
      "notifyTitle": "TokenKey Studio",
      "notifyEnabled": "Notifications on",
      "notifyDenied": "Notifications blocked",
      "resultsTitle": "Videos",
      "play": "Play",
      "playHint": "Click to play",
      "close": "Close",
      "loadingPreview": "Loading…",
      "expiredTitle": "Preview expired",
      "expiredHint": "The video link is a short-lived upstream URL and may have expired. If you just generated it, tap Retry — otherwise regenerate.",
      "expiredHintInline": "In-tab preview failed. Download to save locally, or reuse the prompt to generate again (billed again).",
      "stalled": "Cannot fetch progress (the API key may have been deleted). Please generate again.",
      "retry": "Try again",
      "copyLink": "Copy link",
      "copied": "Copied",
      "inlineCopyHint": "Inline clips cannot be shared as a browser link — download started automatically.",
      "generateAudio": "Generate audio",
      "generateAudioHint": "Include sound in the generated clip (when the model supports it).",
      "retentionHint": "Download now · short local preview; upstream links expire",
      "previewBuffering": "Buffering video…",
      "checkingPreview": "Checking whether in-page preview is available…",
      "downloadOnlyTitle": "Video ready",
      "downloadOnlyHint": "This source cannot be previewed in the browser. Download to watch locally."
    },
    "chat": {
      "model": "Model",
      "temperature": "Temperature",
      "maxTokens": "Max tokens",
      "pickModelPlaceholder": "Select a model…",
      "noModels": "No chat models for this key. Check group routing or pick another key.",
      "systemPrompt": "System prompt (optional)",
      "inputPlaceholder": "Type a message… (Enter to send)",
      "send": "Send",
      "sending": "Sending…",
      "cancel": "Cancel",
      "clear": "Clear chat",
      "emptyHint": "Send a message to see the assistant reply here.",
      "roleUser": "You",
      "roleAssistant": "Assistant",
      "avatarUser": "Me",
      "avatarAssistant": "AI",
      "limitsHint": "Up to {turns} turns in memory; max output tokens capped at {maxTok}; 60s timeout per request.",
      "lastUsage": "Last response usage",
      "promptTokens": "Prompt tokens",
      "completionTokens": "Completion tokens",
      "totalTokens": "Total tokens",
      "cancelled": "Request cancelled.",
      "requestFailed": "Request failed.",
      "integrationsTitle": "Connect external apps",
      "integrationsHint": "Import the gateway address and the selected API key into a client with one click.",
      "integrationsAppHint": "Requires the client app installed (opens a custom URL scheme).",
      "copyBaseUrl": "Copy base URL",
      "copyKey": "Copy API key",
      "baseUrlCopied": "Base URL copied",
      "keyCopied": "API key copied"
    }
  },
  "home": {
    "providers": {
      "newapi": "Extension Engine"
    }
  },
  "common": {
    "chunkLoadFailed": "The page assets are still out of date after refresh. Please clear the browser cache and reload this page."
  },
  "models": {
    "title": "Model Marketplace",
    "subtitle": "Browse and compare AI models by capability, provider, and price",
    "filterAll": "All",
    "filterText": "Text",
    "filterImage": "Image",
    "filterVideo": "Video",
    "searchPlaceholder": "Search models...",
    "pricePerK": "/ 1K tokens",
    "viewPricing": "View Pricing Details",
    "noModels": "No models match your filters.",
    "browseAll": "Browse All Models",
    "inputPrice": "Input",
    "outputPrice": "Output",
    "providers": "Providers",
    "capabilities": {
      "vision": "Vision",
      "tools": "Tools",
      "function_calling": "Function calling",
      "thinking": "Extended thinking",
      "reasoning": "Reasoning",
      "image_generation": "Image generation",
      "video_generation": "Video generation",
      "audio": "Audio",
      "json": "JSON mode",
      "prompt_caching": "Prompt caching"
    }
  },
  "nav": {
    "quickstart": "Quick Start",
    "edgeAccounts": "Edge Accounts",
    "studio": "Studio",
    "modelMarketplace": "Model Marketplace"
  },
  "auth": {
    "turnstileFailedRefresh": "Stale verification token — please refresh this page and try again."
  },
  "keys": {
    "export": "Export",
    "exporting": "Exporting…",
    "exportTooltip": "Export this key's conversation records (training-ready JSONL)",
    "exportSuccess": "Exported {count} conversation records",
    "exportEmpty": "No conversation records captured for this key yet",
    "exportFailed": "Export failed, please try again",
    "exportPanel": {
      "title": "Export Conversations",
      "exportNow": "Export now",
      "exporting": "Exporting…",
      "lastExport": "Last export: {time}",
      "noExports": "No exports yet",
      "download": "Download",
      "expired": "Expired",
      "recordCount": "{count} records",
      "kindManual": "Manual",
      "kindAuto": "Auto",
      "status": "Status",
      "statusValue": {
        "pending": "Pending",
        "running": "Running",
        "done": "Ready",
        "failed": "Failed"
      },
      "close": "Close"
    },
    "universalLabel": "Universal key (all platforms)",
    "universalHint": "One key works across every platform, model and modality you are entitled to. Turn off to lock this key to a single group.",
    "universalBadge": "Universal",
    "useKeyModal": {
      "modelLabel": "Model",
      "baseUrlLabel": "Base URL",
      "keyLabel": "API Key",
      "reveal": "Show",
      "hide": "Hide",
      "testKey": "Test key",
      "testing": "Testing…",
      "testModelOk": "Model reachable — config is correct",
      "testKeyValid": "Key valid (use it inside Claude Code)",
      "modelsLoading": "Loading the models this key can serve…",
      "modelsEmpty": "Could not load the servable model list; you can type a model name manually in the config below",
      "ccOnlyWarning": "This group only accepts Claude Code clients (claude-cli) and only /v1/messages. curl / Python / OpenCode and the like are rejected by the gateway with 403.",
      "cliTabs": {
        "curl": "cURL",
        "python": "Python"
      },
      "claudeCode": {
        "envHint": "Recommended: model claude-opus-4-8[1m]; disables adaptive thinking (avoids silent down-grading); pins thinking budget to 31999 tokens; triggers auto-compact at ~60% context use (CLAUDE_CODE_AUTOCOMPACT_PCT_OVERRIDE). The commented NONESSENTIAL_TRAFFIC flag should only be enabled when routing directly to Anthropic OAuth — otherwise upstream prompt cache TTL drops from 1h to 5min and token cost spikes.",
        "vscodeHint": "Claude Code settings.json with effortLevel=high and all recommended env vars. Replace the file to apply."
      }
    }
  },
  "admin": {
    "dashboard": {
      "promptCacheHitRate": "Prompt Cache Hit Rate",
      "promptCacheHitRateHint": "cache_read / (cache_read + input + cache_create). Higher = better. Sticky routing aims to maximize this.",
      "cacheReadTokens": "Cache Read",
      "cacheCreateTokens": "Cache Created",
      "promptCacheToday": "Today",
      "promptCacheTotal": "Total"
    },
    "users": {
      "form": {
        "trajExport": "Allow conversation export",
        "trajExportHint": "When on, the user can export each API key's captured conversation records individually."
      }
    },
    "groups": {
      "platforms": {
        "newapi": "Extension Engine"
      },
      "openaiMessages": {
        "compactionEnabled": "Enable auto compaction",
        "compactionEnabledHint": "When estimated input tokens exceed the threshold, /v1/messages compaction is applied automatically.",
        "compactionThreshold": "Input token threshold",
        "compactionThresholdPlaceholder": "e.g., 900000",
        "compactionThresholdHint": "Must be >= 1. Choose a value based on model context window.",
        "compactionThresholdRequired": "Please enter a valid compaction threshold (>= 1)"
      }
    },
    "channels": {
      "form": {
        "billingModelSourceConfirm": "Setting the billing basis to bill by requested / final model can make the billed model name differ from what the price gate judges, leaking $0 charges. Continue?"
      }
    },
    "edgeAccounts": {
      "title": "Edge Accounts",
      "description": "Read-only overview of accounts on each edge deployment",
      "refresh": "Refresh",
      "lastFetched": "Last fetched",
      "platformFilter": "Platform",
      "allPlatforms": "All platforms",
      "statusFilter": "Status",
      "allStatus": "All statuses",
      "statusFilterHint": "Normal = both the prod stub and the edge account are healthy; every other status is an OR — shown if EITHER the prod stub or the edge account is in that state.",
      "groupFilter": "Group",
      "allGroups": "All groups",
      "ungroupedGroup": "Ungrouped",
      "groupFilterHint": "Filters by the prod-side stub account's group (how prod organizes this edge), not the edge's own internal account groups.",
      "noMatch": "No accounts match the current filters.",
      "manageAccounts": "Manage accounts",
      "manageFailed": "Failed to open this edge for management",
      "loadFailed": "Failed to load edge accounts",
      "handoff": {
        "signingIn": "Signing in to this edge…",
        "failed": "Sign-in failed or the link expired.",
        "goLogin": "Go to login"
      },
      "noEdges": "No edges discovered (no anthropic mirror stubs configured on this deployment).",
      "edgeEmpty": "No accounts on this edge.",
      "summaryEdges": "{ok}/{total} edges reachable",
      "summaryAccounts": "{count} accounts",
      "summaryFailed": "{count} unreachable",
      "summaryConfigLabel": "Schedulable current/caps ({count} accounts)",
      "summaryConcurrency": "Concurrency {current}/{value}",
      "summaryBaseRpm": "Base RPM {current}/{base} (sticky {sticky})",
      "summarySessions": "Sessions {current}/{value}",
      "accountCount": "{count} accounts",
      "schedulableCount": "{count} schedulable",
      "stubPaused": "Scheduling off",
      "stubPausedHint": "The prod-side stub for this edge is paused (关调度) — prod no longer routes traffic here, though the edge itself stays reachable.",
      "stubRateLimited": "Stub rate-limited",
      "stubRateLimitedHint": "The prod-side stub for this edge is in a rate-limit cooldown — prod's relay to this edge is throttled even if the edge's own accounts are healthy. This is also why the edge still appears under the rate-limited filter.",
      "stubTempUnsched": "Stub temp-unschedulable",
      "stubTempUnschedHint": "The prod-side stub for this edge is in a temp-unschedulable cooldown. This is also why the edge still appears under the temp-unschedulable filter.",
      "cooldownRecovered": "Recovered",
      "accountIdHint": "Account ID on this edge (edge-local database primary key), used to pinpoint it when troubleshooting",
      "columns": {
        "name": "Name",
        "platformType": "Platform / Type",
        "capacity": "Capacity",
        "usageWindows": "Usage Windows",
        "state": "State",
        "priority": "Priority",
        "groups": "Groups",
        "lastUsed": "Last Used"
      }
    },
    "accounts": {
      "accountEmail": "Account email",
      "accountEmailPlaceholder": "Account email (optional)",
      "accountEmailHint": "Used for CC userEmail backfill and ops identification. Auto-filled on OAuth success; you can also edit manually.",
      "invalidAccountEmail": "Please enter a valid account email",
      "kiroStubPlatform": "Kiro Stub",
      "edgePanel": {
        "actions": "Actions",
        "expandAll": "Expand all",
        "collapseAll": "Collapse all",
        "expandHint": "Expand/collapse the edge accounts this stub schedules (all expanded by default; collapse manually)",
        "expandOne": "Expand this stub",
        "collapseOne": "Collapse this stub",
        "summary": "{total} accounts · {schedulable} schedulable",
        "summaryLoading": "Loading…",
        "scopeGroup": "Scheduled from group {group} · {count} total",
        "scopePool": "{platform} whole pool · {count} total",
        "scopeHint": "The accounts this stub api key actually schedules on the edge",
        "retry": "Retry",
        "groupEmpty": "This stub's group has no accounts on the edge yet",
        "queryUsage": "Query usage",
        "clearRateLimit": "Clear rate limit",
        "clearTempUnsched": "Clear temp-unschedulable",
        "resetQuota": "Reset quota",
        "manageOnEdge": "Manage on edge ↗",
        "manageWholeEdge": "Manage all edge accounts ↗",
        "opSuccess": "Done",
        "opFailed": "Operation failed, please retry",
        "queryFailed": "Usage query failed",
        "notDiscovered": "Edge not discovered yet (its prod stub may be disabled)"
      },
      "accountIdHint": "Account ID (database primary key), used to pinpoint this account when troubleshooting",
      "platforms": {
        "newapi": "Extension Engine"
      },
      "setTierDialog": {
        "menuItem": "Set Tier",
        "title": "Set Account Tier",
        "selectLabel": "Select tier",
        "applyButton": "Apply",
        "applySuccess": "Tier applied (this deployment only)",
        "applyFailed": "Failed to apply tier",
        "localScopeWarning": "Applies only to this deployment’s database; other edges / prod still need the ops/anthropic pipeline fan-out."
      },
      "capacity": {
        "rpm": {
          "stickyBufferSuffix": "(+{buffer} sticky)"
        }
      },
      "anthropicVertexLabel": "Anthropic Vertex",
      "vertexSaJsonPastePlaceholder": "Or paste the full Service Account JSON here",
      "vertexSaJsonEditWriteOnceHint": "Leave empty to keep the current Service Account JSON. Paste a new JSON to rotate the key — project_id is re-extracted automatically.",
      "vertexSaJsonEditWriteOncePlaceholder": "Leave empty to keep current — or paste a new Service Account JSON to replace it",
      "vertexNewapiMediaHint": "For Google Cloud trial credits and Imagen / Veo media models, use this path: Extension Engine (newapi) + Vertex AI channel type (ch41). Do not choose the Vertex Service Account card under the Gemini platform.",
      "vertexNewapiServiceAccountHint": "NewAPI Vertex AI channel selected: upload the Service Account JSON to create a ch41 service_account, using the model whitelist below as model_mapping.",
      "openai": {
        "messagesCompactionEnabled": "Enable account-level /v1/messages auto compaction",
        "messagesCompactionEnabledDesc": "When enabled, this account uses its own input token compaction threshold; when disabled, group policy applies.",
        "messagesCompactionThreshold": "Account input token threshold",
        "messagesCompactionThresholdPlaceholder": "e.g., 900000",
        "messagesCompactionThresholdHint": "Must be >= 1. Compaction runs automatically when threshold is exceeded.",
        "messagesCompactionThresholdRequired": "Please enter a valid account compaction threshold (>= 1)"
      },
      "anthropic": {
        "oauthPassthrough": "OAuth auto passthrough (auth only)",
        "oauthPassthroughDesc": "Only applies to Anthropic OAuth/setup-token accounts. When enabled, skips fingerprint/mimic/canonical rewrites and forwards messages/count_tokens with Authorization replacement only, while billing/concurrency/audit and safety filtering are preserved.",
        "mirrorPlatform": "Mirror platform",
        "mirrorPlatformHint": "Only for edge \"mirror stub\" accounts (base URL points at an internal api-<edge>.tokenkey.dev host). Declares which edge pool this stub mirrors its concurrency from: Anthropic (default) or Kiro. Leave Anthropic for normal API Key accounts."
      },
      "concurrencyZeroHint": "0 = unlimited (no concurrency limit)",
      "newApiPlatform": {
        "channelType": "Channel Type",
        "channelTypePlaceholder": "Select an Extension Engine channel type",
        "channelTypeLoadFailed": "Failed to load channel types, please retry",
        "baseUrl": "Base URL",
        "baseUrlHint": "Upstream endpoint, e.g. https://api.deepseek.com",
        "apiKey": "API Key",
        "apiKeyPlaceholder": "sk-...",
        "apiKeyEditHint": "Leave empty to keep current key",
        "pleaseSelectChannelType": "Please select an Extension Engine channel type",
        "pleaseEnterBaseUrl": "Please enter upstream Base URL",
        "pleaseEnterApiKey": "Please enter upstream API Key",
        "models": "Models",
        "modelsHint": "Models this account is allowed to forward; empty means allow all.",
        "fetchUpstreamModels": "Fetch model list",
        "fetchUpstreamModelsHint": "Pull the real model list from upstream /v1/models (or equivalent) and overwrite the current whitelist.",
        "fetchUpstreamModelsNeedUrlKey": "Please fill in Base URL and API Key first",
        "fetchUpstreamModelsEmpty": "Upstream returned no models",
        "fetchUpstreamModelsSuccess": "Fetched {count} models from upstream",
        "fetchUpstreamModelsFailed": "Failed to fetch upstream model list",
        "pricingStatusPriced": "Priced",
        "pricingStatusMissing": "Missing price",
        "statusCodeMapping": "Status Code Mapping (JSON, optional)",
        "statusCodeMappingHint": "Remaps upstream HTTP status codes, for example 404 to 500. Leave empty to pass through.",
        "openaiOrganization": "OpenAI Organization (optional)",
        "openaiOrganizationHint": "Sent as the OpenAI-Organization header on outbound requests. Leave empty to omit.",
        "jsonInvalid": "Must be valid JSON",
        "jsonObjectRequired": "Must be a JSON object",
        "pleaseConfigureModelMapping": "newapi accounts must declare a non-empty model_mapping"
      },
      "kiroPlatform": {
        "tokenJsonLabel": "Kiro auth token JSON",
        "tokenJsonPlaceholder": "Paste ~/.aws/sso/cache/kiro-auth-token.json contents",
        "tokenJsonCreateHint": "Paste the local kiro-auth-token.json export (accessToken, refreshToken, region, authMethod).",
        "tokenJsonEditHint": "Leave empty to keep current access/refresh tokens.",
        "tokenJsonEditWriteOnceHint": "Paste only when rotating tokens; leave empty to keep the values stored on the server.",
        "tokenJsonParsed": "Token JSON parsed successfully.",
        "tokenJsonRequired": "Please paste Kiro auth token JSON",
        "tokenJsonInvalid": "Kiro auth token JSON is not valid JSON",
        "tokenJsonObjectRequired": "Kiro auth token JSON must be an object",
        "tokenJsonMissingFields": "Token JSON must include accessToken and refreshToken",
        "pleasePasteTokenJson": "Please paste valid Kiro auth token JSON",
        "registrationJsonLabel": "IdC client registration JSON",
        "registrationJsonPlaceholder": "Paste ~/.aws/sso/cache/*.json with clientId and clientSecret (IdC only)",
        "registrationJsonCreateHint": "Required for IdC auth. Paste the SSO cache registration file that contains clientId and clientSecret.",
        "registrationJsonEditHint": "Leave empty to keep the current IdC client secret.",
        "registrationJsonEditWriteOnceHint": "Paste only when rotating IdC registration; leave empty to keep stored credentials.",
        "registrationJsonParsed": "Registration parsed (clientId: {clientId}).",
        "registrationJsonRequired": "Please paste IdC registration JSON",
        "registrationJsonInvalid": "IdC registration JSON is not valid JSON",
        "registrationJsonObjectRequired": "IdC registration JSON must be an object",
        "registrationJsonMissingFields": "Registration JSON must include clientId and clientSecret",
        "pleasePasteRegistrationJson": "Please paste valid IdC client registration JSON",
        "region": "Region",
        "regionHint": "AWS region for Kiro, defaults to us-east-1.",
        "authMethod": "Auth Method",
        "authMethodSocial": "Social (social login)",
        "authMethodIdc": "IdC (IAM Identity Center)",
        "authMethodHint": "IdC requires the registration JSON above.",
        "machineId": "Machine ID (optional)",
        "machineIdPlaceholder": "Device fingerprint identifier",
        "machineIdHint": "Optional device fingerprint sent with requests.",
        "profileArn": "Profile ARN (optional)",
        "profileArnPlaceholder": "arn:aws:codewhisperer:...",
        "profileArnHint": "Leave empty to let the backend resolve it automatically.",
        "tosAcknowledge": "I confirm I have read and accept the Kiro terms of service and the compliance risks of using this account.",
        "pleaseAcknowledgeTos": "Please acknowledge the Kiro terms of service before creating the account"
      },
      "grokPlatform": {
        "oauthMode": "OAuth",
        "oauthModeHint": "xAI refresh token",
        "relayMode": "Relay Stub",
        "relayModeHint": "Edge API key",
        "refreshToken": "Refresh Token",
        "refreshTokenPlaceholder": "xAI Grok OAuth refresh token",
        "refreshTokenHint": "On create, TokenKey immediately exchanges it for an access token — a failure here means the token is invalid or the account is not SuperGrok Heavy.",
        "refreshTokenHowTo": "Obtain the refresh_token by running the xAI Grok CLI login on your own machine (loopback OAuth), then paste it here. xAI's public client has no server-side redirect / device-code flow, so the token must be minted out-of-band.",
        "baseUrl": "Base URL (optional)",
        "baseUrlHint": "Defaults to https://api.x.ai/v1. Override only for a self-hosted reverse proxy.",
        "relayBaseUrlHint": "Use the edge gateway URL, for example https://api-us4.tokenkey.dev.",
        "relayApiKeyHint": "Use the TokenKey edge API key for this relay stub.",
        "tokenEditHint": "Leave empty to keep the current value",
        "heavyNote": "xAI gates the OAuth API surface to SuperGrok Heavy. A standard / expired subscription returns HTTP 403 at request time.",
        "pleaseEnterRefreshToken": "Please enter the Grok refresh token"
      },
      "gemini": {
        "accountType": {
          "vertexTitle": "Gemini Vertex",
          "vertexDesc": "Native Vertex for text chat"
        }
      },
      "loadModelsUnavailable": "Couldn't load this account's models — the account is currently unavailable (it may have been deleted or is being re-authorized). Refresh the account list and try again.",
      "loadModelsAuthExpired": "Couldn't load models — your admin session has expired. Sign in again and retry.",
      "loadModelsFailed": "Failed to load this account's models. Please retry.",
      "usageWindow": {
        "upstreamQuota": "Upstream quota",
        "upstreamUnsupported": "Upstream quota not connected",
        "upstreamUnknown": "Upstream quota unknown",
        "kiroCredits": "Credits",
        "kiroTrial": "Trial",
        "kiroTrialExpires": "Trial ends",
        "kiroBonus": "Bonus"
      },
      "openaiQuotaReset": {
        "additionalLimitsTitle": "Per-model limits"
      }
    },
    "ops": {
      "clientFaults": "client faults:",
      "failoverHopStats": {
        "title": "Failover Hop Stats (per account)",
        "hint": "How many hops each recovered request wasted = wasted upstream round-trips; tracks the hop reduction from the #899 window scheduler.",
        "failedToLoad": "Failed to load failover hop stats",
        "empty": "No failover hop stats for the current filters",
        "totalAccounts": "Total accounts: {total}",
        "table": {
          "account": "Account",
          "platform": "Platform",
          "recoveredCount": "Recovered Requests",
          "totalFailoverHops": "Total Failover Hops",
          "totalWastedAttempts": "Total Wasted Attempts",
          "avgHopsPerRecovered": "Avg Hops / Recovered"
        }
      },
      "errorDetail": {
        "apiKey": "API Key",
        "clientIp": "Client IP"
      },
      "requestDetails": {
        "table": {
          "requester": "Requester"
        },
        "requester": {
          "anonymous": "Anonymous",
          "key": "Key",
          "group": "Group",
          "account": "Upstream"
        }
      },
      "alertRules": {
        "metrics": {
          "poolLoadRate": "Account Pool Load Rate (%)",
          "routingCapacityRejectionCount": "No-Available-Account Rejections"
        },
        "metricDescriptions": {
          "poolLoadRate": "Per-pool concurrency load: (in-flight + queued) / total seats. Pools split by platform/group/channel; the most saturated pool wins. ≥100% means queuing. Leading capacity-ceiling signal; ≥90% recommended to trigger adding accounts.",
          "routingCapacityRejectionCount": "Count of \"no available accounts\" routing rejections in the window (empty-pool fast-fail 429 + relayed mirror-edge downstream-capacity rejections, error_phase=routing). These client-visible 429s are excluded from error/success rates and cool no account, so this is the only signal that sees a thin-pool-race rejection storm. An absolute count (not a rate); ≥50 over 5 minutes recommended to trigger adding accounts / scaling."
        }
      },
      "email": {
        "feishuTitle": "Feishu P0 Group Alerts",
        "feishuP0OnlyHint": "Only P0 firing events are sent; P1/P2/P3 and resolved notifications are never sent, and @all is not used.",
        "feishuWebhook": "Feishu Bot Webhook",
        "feishuWebhookHint": "HTTPS webhook only; it will not be returned after saving.",
        "feishuWebhookKeepPlaceholder": "Configured, leave empty to keep",
        "feishuWebhookConfiguredHint": "Configured, leave empty to keep; enter a new value to replace it.",
        "feishuSigningSecret": "Signing secret",
        "feishuSecretOptional": "Optional",
        "feishuSecretKeepPlaceholder": "Configured, leave empty to keep",
        "feishuSecretConfiguredHint": "Configured, leave empty to keep; enter a new value to replace it.",
        "feishuCooldownSeconds": "Cooldown seconds",
        "feishuUpstreamBalanceLowThreshold": "Upstream balance alert threshold (CNY)",
        "feishuUpstreamBalanceLowThresholdHint": "A background sentinel polls upstream channel accounts that expose a public balance API (currently DeepSeek) and sends a pre-emptive Feishu warning when balance drops below this value, before it hits zero. Requires Feishu alerts enabled above.",
        "configured": "Configured",
        "notConfigured": "Not configured",
        "validation": {
          "feishuWebhookRequired": "Feishu P0 alerts require a configured webhook when enabled",
          "feishuWebhookHttps": "Feishu webhook must be an HTTPS URL",
          "feishuRateLimitRange": "Feishu rate limit per hour must be between 1 and 24",
          "feishuCooldownRange": "Feishu cooldown must be between 60 and 86400 seconds",
          "feishuUpstreamBalanceLowThresholdRange": "Upstream balance alert threshold must be between 1 and 1000000"
        }
      },
      "settings": {
        "feishuEditHint": "Edit the Feishu webhook, signing secret, rate limit, and cooldown in the Email Notification card."
      }
    },
    "settings": {
      "coldStart": {
        "title": "New-User Cold Start",
        "description": "Lower the friction for first-time users: optional signup bonus (USD), auto-issued trial API key, and a public pricing page. See docs/approved/user-cold-start.md.",
        "signupBonusEnabled": "Signup Bonus",
        "signupBonusEnabledHint": "Credit each newly registered user with a small USD balance so they can try paid models immediately.",
        "signupBonusBalance": "Bonus Balance (USD)",
        "signupBonusBalanceHint": "Granted once at registration. Default $1.00. Must be ≥ 0; set to 0 to keep the toggle on without crediting.",
        "autoGenerateDefaultToken": "Auto-Issue Trial API Key",
        "autoGenerateDefaultTokenHint": "Create one API key automatically after registration so the user can call the gateway without first opening the keys page.",
        "autoGenerateDefaultTokenName": "Trial Key Name",
        "autoGenerateDefaultTokenNameHint": "Display name for the auto-issued key. Defaults to \"trial\".",
        "pricingCatalogPublic": "Public Pricing Page",
        "pricingCatalogPublicHint": "Expose GET /api/v1/public/pricing and the /pricing route. Disable to return 404 and hide the entry from the landing page."
      },
      "gatewayForwarding": {
        "stickyRouting": "Prompt Cache Sticky Routing",
        "stickyRoutingHint": "Enabled by default. Derives stable prompt_cache_key / metadata.user_id / X-Session-Id and injects them upstream to maximize prompt cache hits. When disabled, every group falls back to passthrough — only forwarding sticky fields the client already sent. See docs/approved/sticky-routing.md.",
        "anthropicRequestNormalize": "Anthropic Request Normalize",
        "anthropicRequestNormalizeHint": "Default on. Fixes two recurring client mistakes on /v1/messages before forwarding: (1) tool_choice given as an OpenAI-style string (\"auto\" / \"required\" / \"none\") is rewritten to Anthropic's required object form; (2) when thinking is enabled together with a tool_choice that forces tool use (any / tool), strips thinking to preserve the forced-tool-use intent. Unknown tool_choice strings are left untouched so the upstream still surfaces the client bug.",
        "openaiAllowClaudeCodeCodexPlugin": "Allow using the Codex plugin in Claude Code",
        "openaiAllowClaudeCodeCodexPluginDesc": "Global switch; only affects OpenAI OAuth accounts that have 'Codex official clients only' enabled. When on, all such accounts additionally allow requests from the Claude Code Codex plugin (exact match on originator=Claude Code) without per-account config; upstream requests remain pass-through."
      }
    },
    "tierTemplates": {
      "title": "Tier Templates",
      "description": "Anthropic OAuth stability tiers (l1..l5). Accounts reference a tier by id; per-tier limits resolve at runtime.",
      "projectionBanner": "Tiers are a projection of the git baseline. Edits here apply immediately and fan out to referencing accounts, but the ops/anthropic pipeline re-asserts these rows from git on its next run — use UI edits for emergency/local changes only.",
      "createTier": "Create Tier",
      "editTier": "Edit Tier",
      "deleteTier": "Delete Tier",
      "noTiers": "No tiers configured",
      "columns": {
        "name": "Name",
        "concurrency": "Concurrency",
        "baseRpm": "Base RPM",
        "maxSessions": "Max Sessions",
        "tlsProfile": "TLS Profile",
        "actions": "Actions"
      },
      "form": {
        "name": "Name",
        "description": "Description",
        "concurrency": "Concurrency",
        "priority": "Priority",
        "priorityHint": "Projection only — the window pipeline owns accounts.priority",
        "rateMultiplier": "Rate Multiplier",
        "baseRpm": "Base RPM",
        "maxSessions": "Max Sessions",
        "rpmStickyBuffer": "RPM Sticky Buffer",
        "sessionIdleTimeoutMinutes": "Session Idle Timeout (min)",
        "windowCostLimit": "Window Cost Limit",
        "windowCostStickyReserve": "Window Cost Sticky Reserve",
        "cacheTtlOverrideEnabled": "Cache TTL Override",
        "tlsProfileName": "TLS Profile Name",
        "tlsProfileNameHint": "Pipeline upserts the profile by name and backfills the id",
        "tlsProfileId": "TLS Profile ID"
      },
      "deleteConfirmMessage": "Are you sure you want to delete tier \"{name}\"? Accounts bound to it will fall back to their persisted extra values.",
      "createSuccess": "Tier created successfully",
      "updateSuccess": "Tier updated successfully",
      "deleteSuccess": "Tier deleted successfully",
      "loadFailed": "Failed to load tiers",
      "saveFailed": "Failed to save tier",
      "deleteFailed": "Failed to delete tier"
    }
  },
  "quickstart": {
    "title": "Quick Start",
    "subtitle": "Pick an API key and model, then copy client-specific config to get started.",
    "selectKey": "Select API Key",
    "noKeys": "You have no API keys yet. Create one before configuring a client.",
    "createKey": "Create API Key",
    "manageKeys": "Manage Keys",
    "viewPricing": "View Pricing",
    "tryStudio": "Try Studio"
  }
}

const zh: LocaleOverlay = {
  "pricing": {
    "title": "模型价格",
    "subtitle": "按调用量计费 · USD / 1K tokens",
    "description": "价格以美元 (USD) 计价，单位为每 1,000 tokens。Cache Read / Write 仅对单独计费缓存的模型生效；能力标签来自上游声明（视觉、工具调用等）。",
    "nav": {
      "aria": "离开价格页",
      "home": "首页",
      "console": "控制台",
      "consoleTitleAuthed": "前往您的控制台",
      "consoleTitleGuest": "登录后进入控制台"
    },
    "columns": {
      "model": "模型",
      "vendor": "厂商",
      "input": "输入",
      "output": "输出",
      "cacheRead": "缓存读取",
      "cacheWrite": "缓存写入",
      "contextWindow": "上下文窗口",
      "maxOutput": "最大输出",
      "capabilities": "能力"
    },
    "tableHint": "可左右滑动或横向滚动查看全部列；左侧模型名称支持换行显示。",
    "export": {
      "button": "导出 CSV",
      "success": "定价已导出",
      "empty": "对外价目录为空，无可导出内容"
    },
    "tieredBadge": "阶梯 ×{n}",
    "footer": {
      "total": "共 {count} 个模型",
      "filtered": "显示 {shown} / {total} 个模型"
    },
    "perThousandTokens": "/ 1K tokens",
    "thinkingOutput": "思考",
    "contextTokens": "{count} tokens",
    "updatedAt": "更新于 {time}",
    "empty": {
      "title": "价格目录正在准备中",
      "hint": "暂无模型价格数据，请稍后再来或联系管理员。"
    },
    "errorTitle": "加载价格失败",
    "errorHint": "请刷新页面或稍后重试。",
    "retry": "重试",
    "search": {
      "placeholder": "按模型名称搜索…",
      "modeLabel": "匹配方式",
      "modeFuzzy": "模糊（包含）",
      "modeExact": "精准（全名）",
      "resultCount": "{count} 个模型",
      "noMatches": "没有匹配的模型。可切换到模糊搜索或缩短关键词。"
    },
    "filters": {
      "apiKey": "API Key",
      "keyPlaceholder": "全部 Key",
      "group": "Group",
      "publicCatalog": "所有分组",
      "groupExclusiveOption": "{group}（专属）",
      "search": "搜索",
      "activePublic": "正在查看所有分组",
      "activeGroup": "正在查看 {group} 的分组目录",
      "activeKeyGroup": "正在查看 {key} · {group}"
    },
    "ctaBonus": "立即注册 · 获赠 {amount} 试用额度",
    "ctaBonusHint": "当本站开启注册赠额时显示此提示。",
    "perRequest": "/ 次",
    "perImage": "/ 张",
    "perSecond": "/ 秒",
    "videoClipExample": "5秒 {five} · 10秒 {ten}",
    "modality": {
      "all": "全部",
      "text": "文本",
      "image": "图片",
      "video": "视频"
    },
    "my": {
      "tabMy": "分组目录",
      "tabPublic": "所有分组",
      "title": "分组模型目录",
      "subtitle": "当前 API Key 所属分组可调用的模型与官方定价",
      "description": "展示所选分组可调用的模型及其官方定价，单位为每 1,000 tokens。切换 API Key 查看不同 group，或对比其他可用 group。",
      "pickerKey": "当前 Key：",
      "pickerCompare": "对比其他 group：",
      "compareDefault": "保持当前",
      "noKeyHint": "你还没有可用的 API Key，先去控制台创建一个。",
      "columns": {
        "input": "输入（官方单价）",
        "output": "输出（官方单价）",
        "authorizedGroups": "授权分组"
      },
      "authorizedGroups": {
        "groupHint": "{group} 可服务此模型",
        "exclusive": "专属",
        "quickstart": "快速开始"
      },
      "empty": {
        "noAccess": {
          "title": "暂无可用分组",
          "hint": "没有可访问的 group，请联系管理员或先在订阅页选购。"
        },
        "noModels": {
          "title": "此分组暂未上架模型",
          "hint": "管理员可能正在配置渠道，请稍后再来或切换到其他 group。"
        }
      },
      "exploreBanner": {
        "message": "正在查看 {group} 的目录",
        "cta": "在 {group} 创建 Key"
      },
      "billingMode": {
        "per_request": "按次",
        "image": "按图"
      }
    }
  },
  "studio": {
    "title": "工作室",
    "subtitle": "你的密钥能做的，都在这里试——对话、图片、视频，一处搞定。",
    "balance": "余额",
    "apiKey": "API 密钥",
    "pickKeyPlaceholder": "选择一个 API 密钥…",
    "loadingModels": "正在加载模型…",
    "loadFailed": "工作室加载失败。",
    "noApiKey": "没有可用的 API 密钥，请先在「API 密钥」里创建一个。",
    "manageKeys": "管理 API 密钥",
    "defaultGroup": "默认分组",
    "modeChat": "对话",
    "modeImage": "图片",
    "modeVideo": "视频",
    "modeBakeoff": "同台对比",
    "clear": "清空",
    "topUp": "去充值",
    "viewPricing": "查看价格",
    "via": "经 {vendor}",
    "keyNoModality": "此模式无可用模型",
    "saveReminderTitle": "请立即下载保存",
    "saveReminder": "生成结果只存在本机浏览器（约 7 天）。清缓存、无痕模式或换设备后预览会丢失——请下载到本地留存。",
    "universalKeyBadge": "全能 Key",
    "playback": {
      "inlineLocal": "本机缓存 · 刷新后仍可预览（约 7 天）",
      "upstreamCorsOk": "上游直链 · 已探测可缓存 · 仍请下载",
      "upstreamCorsBlocked": "上游直链 · 跨域不可缓存 · 刷新后不可预览 · 请立即下载",
      "expired": "预览已失效 · 仅保留提示词",
      "unknown": "存储方式未知 · 请立即下载",
      "label": "预览来源"
    },
    "needsApikeyAccount": "需要 apikey 类型账号",
    "badge": {
      "draft": "草稿",
      "standard": "标准",
      "ultra": "极致",
      "fast": "快速",
      "cinematic": "电影级"
    },
    "advanced": {
      "toggle": "高级",
      "negativePrompt": "不要出现",
      "negativePromptHint": "描述要避免的元素（可选）",
      "seed": "随机种子",
      "seedHint": "留空则随机",
      "firstFrame": "首帧图片（URL）",
      "firstFrameHint": "粘贴一张图片 URL，作为视频首帧（可选）"
    },
    "bakeoff": {
      "hint": "一条 prompt，多个模型并排出片——各自带真实价格和速度。",
      "needTwo": "该分组至少需要两个已定价模型才能对比。",
      "promptPlaceholder": "描述要跨模型生成的内容…",
      "pickModels": "对比：",
      "run": "同台生成 {count} 个",
      "running": "生成中…",
      "regenerate": "重新生成",
      "regenerateChanged": "用新 prompt 生成",
      "clearResults": "清空当前展示",
      "historyTitle": "历史对比",
      "maxModelsHint": "最多同时对比 {max} 个模型，取消一个后可再选。",
      "totalCost": "合计 ≈ {cost}",
      "cannotAfford": "余额不足以跑这次对比。",
      "generating": "生成中…",
      "failed": "失败",
      "panelError": "生成失败"
    },
    "cost": {
      "thisGeneration": "本次生成",
      "thisVideo": "这个视频",
      "estimate": "预估",
      "balance": "余额",
      "afterGeneration": "生成后"
    },
    "errors": {
      "insufficient_balance": "余额不足——充值后即可生成。",
      "permission": "该分组未开通此类生成。",
      "unpriced": "该模型当前不可用于生成。",
      "rate_limited": "请求过于频繁，请稍后再试。",
      "unauthorized": "API 密钥无效或已过期。",
      "unsupported_model": "当前密钥/分组不支持该模型，请换分组密钥或取消勾选该模型。",
      "generic": "生成失败，请重试。"
    },
    "image": {
      "modelLabel": "模型",
      "modelEmpty": "当前分组暂无可用的图片模型。",
      "samplePrompt": "东京雨夜的霓虹小巷，电影感，浅景深",
      "perImageUnit": " /张",
      "promptPlaceholder": "描述你想要的图片…",
      "aspectLabel": "比例",
      "billedAs": "按 {tier} 计费 · ×{mult}",
      "billedFlat": "按张计费（固定单价，每次一张）",
      "count": "数量",
      "resultsTitle": "结果",
      "emptyHint": "选个档位并生成——结果会显示在这里。",
      "download": "下载",
      "downloadAll": "下载全部",
      "usePrompt": "用此 prompt",
      "useAsInput": "用作输入",
      "enlargeHint": "点击放大预览",
      "expiredReload": "本机预览已过期——可下载保存，或用提示词重新生成（需再次计费）。",
      "close": "关闭",
      "inputImageLabel": "输入图片（图生图）",
      "inputUpload": "上传图片",
      "inputRemove": "移除",
      "inputHint": "上传或在下方结果里选一张图作为底图，然后在上方描述要修改的地方（仅 Gemini 原生图模型支持）。",
      "reversePrompt": "从图片生成提示词",
      "reversing": "识别中…",
      "reverseEmpty": "未能从图片生成提示词。",
      "revisedPromptHint": "模型实际使用的提示词：{text}",
      "generate": "生成 · {cost}",
      "generateTopUp": "生成 · {cost} — 去充值",
      "generating": "生成中…",
      "noResult": "网关未返回图片数据。",
      "formula": "{base} × {tier} ×{mult} × {n}",
      "formulaFlat": "{base} × {n}"
    },
    "video": {
      "modelLabel": "模型",
      "modelEmpty": "当前分组暂无可用的视频模型。",
      "modelEmptySwitchKey": "部分 API 密钥分组提供视频模型（如 Vertex、VolcEngine、Grok）。请在上方切换到带「视频」能力的密钥。",
      "modelEmptyAllKeys": "你的所有密钥分组均未开通视频模型。可在价格页查看支持的分组，或联系管理员开通。",
      "samplePrompt": "霓虹东京小巷，慢推镜头，雨，反射光，电影感",
      "perSecondUnit": " /秒",
      "promptPlaceholder": "描述你想要的视频…",
      "duration": "时长",
      "aspect": "宽高比",
      "aspectAuto": "默认",
      "emptyHint": "描述视频并生成——提交后可离开，好了会在这里播放。",
      "statusProcessing": "生成中",
      "statusSucceeded": "就绪",
      "statusRefunded": "失败 — 已退款 {cost}",
      "stepSubmitted": "已提交",
      "stepGenerating": "生成中",
      "stepReady": "就绪",
      "reserved": "已预留 {cost} · 失败则退款",
      "notifyMe": "完成通知我",
      "usuallyTakes": "通常 30–90 秒",
      "noUrlHint": "任务已成功。视频结果不会长期保留，此处无法播放——如需查看请重新生成。",
      "expiredReload": "链接已过期，仅保留提示词——可复用提示词重新生成。",
      "download": "下载",
      "failedRefunded": "生成失败 — 已退你 {cost}。试试更短的片子或别的档位。",
      "techDetails": "技术详情",
      "refundLine": "失败的视频全额退款",
      "generate": "生成视频 · {cost}",
      "generateTopUp": "生成 · {cost} — 去充值",
      "submitting": "提交中…",
      "formula": "{rate}/秒 × {seconds} 秒",
      "noTaskId": "提交未返回任务 id。",
      "notifyTitle": "TokenKey 工作室",
      "notifyEnabled": "已开启完成通知",
      "notifyDenied": "通知被屏蔽",
      "resultsTitle": "视频",
      "play": "播放",
      "playHint": "点击播放",
      "close": "关闭",
      "loadingPreview": "加载中…",
      "expiredTitle": "预览已过期",
      "expiredHint": "视频链接是上游的临时地址、有效期较短，可能已过期。若是刚生成的请点重试，否则请重新生成。",
      "expiredHintInline": "本机预览加载失败。请点下载保存到本地，或用提示词重新生成（需再次计费）。",
      "stalled": "无法继续获取进度（密钥可能已删除）。请重新生成。",
      "retry": "重试",
      "copyLink": "复制链接",
      "copied": "已复制",
      "inlineCopyHint": "内联视频不能复制成浏览器链接，已自动开始下载。",
      "generateAudio": "生成音频",
      "generateAudioHint": "为成片附带声音（模型支持时生效）。",
      "retentionHint": "请立即下载保存 · 本机可短期预览，上游链接会过期",
      "previewBuffering": "正在缓冲视频…",
      "checkingPreview": "正在检查能否在页面内预览…",
      "downloadOnlyTitle": "视频已生成",
      "downloadOnlyHint": "此来源不支持在页面内播放，请下载到本地观看。"
    },
    "chat": {
      "model": "模型",
      "temperature": "温度",
      "maxTokens": "Max tokens",
      "pickModelPlaceholder": "请选择模型…",
      "noModels": "该密钥无可用对话模型，请检查分组路由或更换密钥。",
      "systemPrompt": "系统提示词（可选）",
      "inputPlaceholder": "输入消息…（Enter 发送）",
      "send": "发送",
      "sending": "发送中…",
      "cancel": "取消",
      "clear": "清空会话",
      "emptyHint": "发送一条消息，助手回复将显示在此。",
      "roleUser": "你",
      "roleAssistant": "助手",
      "avatarUser": "我",
      "avatarAssistant": "AI",
      "limitsHint": "浏览器内最多保留 {turns} 轮对话；单次输出上限 {maxTok} tokens；单次请求超时 60 秒。",
      "lastUsage": "上次响应用量",
      "promptTokens": "输入 tokens",
      "completionTokens": "输出 tokens",
      "totalTokens": "总计 tokens",
      "cancelled": "请求已取消。",
      "requestFailed": "请求失败。",
      "integrationsTitle": "一键接入外部客户端",
      "integrationsHint": "将网关地址与当前选中的 API 密钥一键导入客户端。",
      "integrationsAppHint": "需已安装对应客户端（通过自定义 URL Scheme 打开）。",
      "copyBaseUrl": "复制接入地址",
      "copyKey": "复制 API 密钥",
      "baseUrlCopied": "接入地址已复制",
      "keyCopied": "API 密钥已复制"
    }
  },
  "home": {
    "providers": {
      "newapi": "扩展引擎"
    }
  },
  "common": {
    "chunkLoadFailed": "页面资源刷新后仍然过期，请清理浏览器缓存后重新加载本页面。"
  },
  "models": {
    "title": "模型市场",
    "subtitle": "按能力、供应商和价格浏览对比 AI 模型",
    "filterAll": "全部",
    "filterText": "文本",
    "filterImage": "图片",
    "filterVideo": "视频",
    "searchPlaceholder": "搜索模型...",
    "pricePerK": "/ 1K tokens",
    "viewPricing": "查看定价详情",
    "noModels": "没有匹配的模型。",
    "browseAll": "浏览全部模型",
    "inputPrice": "输入",
    "outputPrice": "输出",
    "providers": "供应商",
    "capabilities": {
      "vision": "图像输入",
      "tools": "工具调用",
      "function_calling": "工具调用",
      "thinking": "深度思考",
      "reasoning": "深度思考",
      "image_generation": "图像生成",
      "video_generation": "视频生成",
      "audio": "音频",
      "json": "JSON 模式",
      "prompt_caching": "提示缓存"
    }
  },
  "nav": {
    "quickstart": "快速开始",
    "edgeAccounts": "Edge 账号",
    "studio": "工作室",
    "modelMarketplace": "模型市场"
  },
  "auth": {
    "turnstileFailedRefresh": "验证 token 已失效（通常是页面停留过久）—— 请刷新本页后重试。"
  },
  "keys": {
    "export": "导出",
    "exporting": "导出中…",
    "exportTooltip": "导出该 Key 的对话记录（训练就绪 JSONL）",
    "exportSuccess": "已导出 {count} 条对话记录",
    "exportEmpty": "该 Key 暂无可导出的对话记录",
    "exportFailed": "导出失败，请重试",
    "exportPanel": {
      "title": "导出对话",
      "exportNow": "立即导出",
      "exporting": "导出中…",
      "lastExport": "上次导出：{time}",
      "noExports": "暂无导出记录",
      "download": "下载",
      "expired": "已过期",
      "recordCount": "{count} 条",
      "kindManual": "立即",
      "kindAuto": "自动",
      "status": "状态",
      "statusValue": {
        "pending": "排队中",
        "running": "处理中",
        "done": "已就绪",
        "failed": "失败"
      },
      "close": "关闭"
    },
    "universalLabel": "全能 Key（全平台）",
    "universalHint": "一把 key 通你被授权的所有平台、模型与模态。关闭则把这把 key 锁定到单个分组。",
    "universalBadge": "全能",
    "useKeyModal": {
      "modelLabel": "模型",
      "baseUrlLabel": "地址",
      "keyLabel": "密钥",
      "reveal": "显示",
      "hide": "隐藏",
      "testKey": "测试密钥",
      "testing": "测试中…",
      "testModelOk": "模型可用，配置正确",
      "testKeyValid": "密钥有效（请在 Claude Code 内使用）",
      "modelsLoading": "正在加载该密钥可服务的模型…",
      "modelsEmpty": "未能加载可服务模型清单，可在下方配置中手动填写模型名",
      "ccOnlyWarning": "此分组仅允许 Claude Code 客户端（claude-cli），且仅 /v1/messages。curl / Python / OpenCode 等会被网关以 403 拒绝。",
      "cliTabs": {
        "curl": "cURL",
        "python": "Python"
      },
      "claudeCode": {
        "envHint": "推荐配置：模型 claude-opus-4-8[1m]；禁用动态思考(防降智)；固定 31999 tokens 思考预算；约 60% 上下文占用时自动压缩（CLAUDE_CODE_AUTOCOMPACT_PCT_OVERRIDE）。注释掉的 NONESSENTIAL_TRAFFIC 标志仅在直连 Anthropic OAuth 时才考虑开启，否则会让上游 prompt cache TTL 从 1h 降到 5min，token 消耗暴涨。",
        "vscodeHint": "Claude Code settings.json：包含 effortLevel=high 与全部推荐 env，覆盖即可生效。"
      }
    }
  },
  "admin": {
    "dashboard": {
      "promptCacheHitRate": "Prompt Cache 命中率",
      "promptCacheHitRateHint": "命中率 = cache_read / (cache_read + input + cache_create)。值越高越好。粘性路由的目标就是把这一项尽量推高。",
      "cacheReadTokens": "命中缓存",
      "cacheCreateTokens": "写入缓存",
      "promptCacheToday": "今日",
      "promptCacheTotal": "累计"
    },
    "users": {
      "form": {
        "trajExport": "允许导出对话记录",
        "trajExportHint": "开启后，该用户每个 API Key 可单独导出其捕获的对话记录"
      }
    },
    "groups": {
      "platforms": {
        "newapi": "扩展引擎"
      },
      "openaiMessages": {
        "compactionEnabled": "启用自动压缩",
        "compactionEnabledHint": "输入 token 估算超过阈值时，自动触发 /v1/messages 压缩。",
        "compactionThreshold": "输入 token 阈值",
        "compactionThresholdPlaceholder": "例如: 900000",
        "compactionThresholdHint": "必须大于等于 1，建议按模型上下文窗口设置。",
        "compactionThresholdRequired": "请填写有效的压缩阈值（>= 1）"
      }
    },
    "channels": {
      "form": {
        "billingModelSourceConfirm": "将「计费基准」设为按请求模型 / 最终模型计费，可能导致计费用的模型名与价格闸判定的不一致而产生 $0 漏计。确认继续吗？"
      }
    },
    "edgeAccounts": {
      "title": "Edge 账号",
      "description": "只读查看各 edge 部署下的账号情况",
      "refresh": "刷新",
      "lastFetched": "最近拉取",
      "platformFilter": "平台",
      "allPlatforms": "全部平台",
      "statusFilter": "状态",
      "allStatus": "全部状态",
      "statusFilterHint": "正常 = prod stub 与 edge 账号都正常；其余状态为「或」关系——prod stub 或 edge 账号任一命中即显示。",
      "groupFilter": "分组",
      "allGroups": "全部分组",
      "ungroupedGroup": "未分配分组",
      "groupFilterHint": "按 prod 侧 stub 账号所属分组筛选（即 prod 如何编排该 edge），而非 edge 内部账号的分组。",
      "noMatch": "没有符合当前筛选条件的账号。",
      "manageAccounts": "管理账号",
      "manageFailed": "打开该 edge 管理页失败",
      "loadFailed": "加载 edge 账号失败",
      "handoff": {
        "signingIn": "正在登录该 edge…",
        "failed": "登录失败或链接已过期。",
        "goLogin": "去登录"
      },
      "noEdges": "未发现任何 edge（本部署未配置 anthropic mirror stub）。",
      "edgeEmpty": "该 edge 下没有账号。",
      "summaryEdges": "{ok}/{total} 个 edge 可达",
      "summaryAccounts": "共 {count} 个账号",
      "summaryFailed": "{count} 个不可达",
      "summaryConfigLabel": "可调度账号当前/容量合计（{count} 个可调度）",
      "summaryConcurrency": "并发 {current}/{value}",
      "summaryBaseRpm": "base RPM {current}/{base}（粘性 {sticky}）",
      "summarySessions": "会话 {current}/{value}",
      "accountCount": "{count} 个账号",
      "schedulableCount": "{count} 个可调度",
      "stubPaused": "调度已关闭",
      "stubPausedHint": "该 edge 在 prod 侧的 stub 已关调度——prod 不再向其调度流量，但该 edge 本身仍可达。",
      "stubRateLimited": "stub 限流中",
      "stubRateLimitedHint": "该 edge 在 prod 侧的 stub 正处于限流冷却中——prod 到该 edge 的中继被限流（即使 edge 内部账号正常）。这也是按「限流中」筛选时该 edge 仍出现的原因。",
      "stubTempUnsched": "stub 临时不可调度",
      "stubTempUnschedHint": "该 edge 在 prod 侧的 stub 正处于临时不可调度冷却中。这也是按「临时不可调度」筛选时该 edge 仍出现的原因。",
      "cooldownRecovered": "已恢复",
      "accountIdHint": "该 edge 上账号的 ID（edge 本地数据库主键），排查问题时用于精确定位",
      "columns": {
        "name": "名称",
        "platformType": "平台 / 类型",
        "capacity": "容量",
        "usageWindows": "用量窗口",
        "state": "状态",
        "priority": "优先级",
        "groups": "分组",
        "lastUsed": "最近使用"
      }
    },
    "accounts": {
      "accountEmail": "账号邮箱",
      "accountEmailPlaceholder": "账号邮箱（可选）",
      "accountEmailHint": "用于 CC userEmail 回填与运维识别；OAuth 授权成功时会自动写入，也可手动补全或修改。",
      "invalidAccountEmail": "请输入有效的账号邮箱",
      "kiroStubPlatform": "Kiro Stub",
      "edgePanel": {
        "actions": "操作",
        "expandAll": "展开全部",
        "collapseAll": "折叠全部",
        "expandHint": "展开/折叠该 stub 背后调度的 edge 账号（默认全部展开，可手动折叠）",
        "expandOne": "展开此 stub",
        "collapseOne": "折叠此 stub",
        "summary": "{total} 账号 · {schedulable} 可调度",
        "summaryLoading": "加载中…",
        "scopeGroup": "调度自 {group} 组 · 共 {count} 个",
        "scopePool": "{platform} 全池 · 共 {count} 个",
        "scopeHint": "这把 stub api key 在该 edge 上实际调度的账号范围",
        "retry": "重试",
        "groupEmpty": "该 stub 的分组在此 edge 上暂无账号",
        "queryUsage": "查询用量",
        "clearRateLimit": "清除限流",
        "clearTempUnsched": "重置临时不可调度",
        "resetQuota": "重置配额",
        "manageOnEdge": "在 edge 后台管理 ↗",
        "manageWholeEdge": "管理该 edge 全部账号 ↗",
        "opSuccess": "操作成功",
        "opFailed": "操作失败，请稍后重试",
        "queryFailed": "用量查询失败",
        "notDiscovered": "该 edge 暂未被发现（其 prod stub 可能已禁用）"
      },
      "accountIdHint": "账号 ID（数据库主键），排查问题时用于精确定位该账号",
      "setTierDialog": {
        "menuItem": "设置 Tier",
        "title": "设置账号 Tier",
        "selectLabel": "选择 Tier 档位",
        "applyButton": "应用",
        "applySuccess": "Tier 已应用（仅当前部署生效）",
        "applyFailed": "Tier 应用失败",
        "localScopeWarning": "仅对当前部署的数据库生效；其它 edge / prod 仍需通过 ops/anthropic 流水线扇出。"
      },
      "capacity": {
        "rpm": {
          "stickyBufferSuffix": "(+{buffer} 粘性)"
        }
      },
      "platforms": {
        "newapi": "扩展引擎"
      },
      "usageWindow": {
        "upstreamQuota": "上游配额",
        "upstreamUnsupported": "未接入上游配额",
        "upstreamUnknown": "上游配额未知",
        "kiroCredits": "额度",
        "kiroTrial": "试用",
        "kiroTrialExpires": "试用到期",
        "kiroBonus": "奖励"
      },
      "openaiQuotaReset": {
        "additionalLimitsTitle": "分模型限额"
      },
      "anthropicVertexLabel": "Anthropic Vertex",
      "vertexSaJsonPastePlaceholder": "或在此粘贴完整的 Service Account JSON",
      "vertexSaJsonEditWriteOnceHint": "留空则保留当前 Service Account JSON；粘贴新的 JSON 可轮换密钥，project_id 会自动重新解析。",
      "vertexSaJsonEditWriteOncePlaceholder": "留空保留当前；或粘贴新的 Service Account JSON 以替换",
      "vertexNewapiMediaHint": "Google Cloud 试用额度、Imagen / Veo 媒体模型必须走这里：扩展引擎（newapi）+ 渠道类型 Vertex AI（ch41）。不要选 Gemini 平台里的 Vertex Service Account。",
      "vertexNewapiServiceAccountHint": "已选择 newapi Vertex AI 渠道：上传 Service Account JSON 后会创建 ch41 service_account，并使用下方模型白名单作为 model_mapping。",
      "openai": {
        "messagesCompactionEnabled": "启用账号级 /v1/messages 自动压缩",
        "messagesCompactionEnabledDesc": "开启后，当前账号可单独设置输入 token 压缩阈值；关闭则回退到分组策略。",
        "messagesCompactionThreshold": "账号级输入 token 阈值",
        "messagesCompactionThresholdPlaceholder": "例如: 900000",
        "messagesCompactionThresholdHint": "必须大于等于 1，超过阈值后自动执行压缩。",
        "messagesCompactionThresholdRequired": "请填写有效的账号级压缩阈值（>= 1）"
      },
      "anthropic": {
        "oauthPassthrough": "OAuth 自动透传（仅替换认证）",
        "oauthPassthroughDesc": "仅对 Anthropic OAuth/setup-token 生效。开启后跳过 fingerprint、mimic、canonical 等改写，messages/count_tokens 仅替换 Authorization；保留计费/并发/审计及必要安全过滤。",
        "mirrorPlatform": "镜像平台",
        "mirrorPlatformHint": "仅用于对接 edge 的「镜像 stub」账号（base URL 指向内部 api-<edge>.tokenkey.dev）。声明该 stub 从 edge 的哪个池镜像并发：Anthropic（默认）或 Kiro。普通 API Key 账号保持 Anthropic 即可。"
      },
      "concurrencyZeroHint": "0 = 不限制（无并发上限）",
      "newApiPlatform": {
        "channelType": "渠道类型",
        "channelTypePlaceholder": "请选择扩展引擎渠道类型",
        "channelTypeLoadFailed": "加载渠道类型失败，请重试",
        "baseUrl": "Base URL",
        "baseUrlHint": "上游服务地址，例如 https://api.deepseek.com",
        "apiKey": "API Key",
        "apiKeyPlaceholder": "sk-...",
        "apiKeyEditHint": "留空表示保留当前密钥",
        "pleaseSelectChannelType": "请选择扩展引擎渠道类型",
        "pleaseEnterBaseUrl": "请输入上游 Base URL",
        "pleaseEnterApiKey": "请输入上游 API Key",
        "models": "模型",
        "modelsHint": "允许该账号转发的模型；留空表示允许所有。",
        "fetchUpstreamModels": "获取模型列表",
        "fetchUpstreamModelsHint": "从上游 /v1/models（或对等接口）拉取真实模型列表，覆盖当前白名单。",
        "fetchUpstreamModelsNeedUrlKey": "请先填入 Base URL 与 API Key",
        "fetchUpstreamModelsEmpty": "上游未返回任何模型",
        "fetchUpstreamModelsSuccess": "已获取 {count} 个上游模型",
        "fetchUpstreamModelsFailed": "获取上游模型列表失败",
        "pricingStatusPriced": "已定价",
        "pricingStatusMissing": "缺定价",
        "statusCodeMapping": "状态码映射（JSON，可选）",
        "statusCodeMappingHint": "将上游 HTTP 状态码改写为另一个值，例如 404 改为 500。留空则透传。",
        "openaiOrganization": "OpenAI Organization（可选）",
        "openaiOrganizationHint": "在出站请求上设置 OpenAI-Organization 请求头。留空则不发送。",
        "jsonInvalid": "不是合法 JSON",
        "jsonObjectRequired": "必须是 JSON 对象",
        "pleaseConfigureModelMapping": "newapi 账号必须声明非空 model_mapping"
      },
      "kiroPlatform": {
        "tokenJsonLabel": "Kiro auth token JSON",
        "tokenJsonPlaceholder": "粘贴 ~/.aws/sso/cache/kiro-auth-token.json 内容",
        "tokenJsonCreateHint": "粘贴本机 kiro-auth-token.json（含 accessToken、refreshToken、region、authMethod）。",
        "tokenJsonEditHint": "留空表示保留当前 access/refresh token。",
        "tokenJsonEditWriteOnceHint": "仅在轮换 token 时粘贴；留空则保留服务端已有值。",
        "tokenJsonParsed": "Token JSON 解析成功。",
        "tokenJsonRequired": "请粘贴 Kiro auth token JSON",
        "tokenJsonInvalid": "Kiro auth token JSON 不是合法 JSON",
        "tokenJsonObjectRequired": "Kiro auth token JSON 必须是 JSON 对象",
        "tokenJsonMissingFields": "Token JSON 须包含 accessToken 与 refreshToken",
        "pleasePasteTokenJson": "请粘贴有效的 Kiro auth token JSON",
        "registrationJsonLabel": "IdC client registration JSON",
        "registrationJsonPlaceholder": "粘贴含 clientId、clientSecret 的 ~/.aws/sso/cache/*.json（仅 IdC）",
        "registrationJsonCreateHint": "IdC 认证必填。粘贴 SSO cache 里含 clientId、clientSecret 的 registration 文件。",
        "registrationJsonEditHint": "留空表示保留当前 IdC client secret。",
        "registrationJsonEditWriteOnceHint": "仅在轮换 IdC registration 时粘贴；留空则保留服务端已有值。",
        "registrationJsonParsed": "Registration 已解析（clientId: {clientId}）。",
        "registrationJsonRequired": "请粘贴 IdC registration JSON",
        "registrationJsonInvalid": "IdC registration JSON 不是合法 JSON",
        "registrationJsonObjectRequired": "IdC registration JSON 必须是 JSON 对象",
        "registrationJsonMissingFields": "Registration JSON 须包含 clientId 与 clientSecret",
        "pleasePasteRegistrationJson": "请粘贴有效的 IdC client registration JSON",
        "region": "Region",
        "regionHint": "Kiro 使用的 AWS region，默认 us-east-1。",
        "authMethod": "认证方式",
        "authMethodSocial": "Social（社交登录）",
        "authMethodIdc": "IdC（IAM Identity Center）",
        "authMethodHint": "IdC 方式需粘贴上方 registration JSON。",
        "machineId": "Machine ID（可选）",
        "machineIdPlaceholder": "设备指纹标识",
        "machineIdHint": "可选的设备指纹，随请求一起发送。",
        "profileArn": "Profile ARN（可选）",
        "profileArnPlaceholder": "arn:aws:codewhisperer:...",
        "profileArnHint": "留空则由后端自动获取。",
        "tosAcknowledge": "我已确认阅读并接受 Kiro 服务条款，并知悉使用该账号的合规风险。",
        "pleaseAcknowledgeTos": "创建账号前请先勾选确认 Kiro 服务条款"
      },
      "grokPlatform": {
        "oauthMode": "OAuth",
        "oauthModeHint": "xAI refresh token",
        "relayMode": "Relay Stub",
        "relayModeHint": "Edge API key",
        "refreshToken": "Refresh Token",
        "refreshTokenPlaceholder": "xAI Grok OAuth refresh token",
        "refreshTokenHint": "创建时 TokenKey 会立刻用它换取 access token —— 这里若失败，说明 token 无效或该账号不是 SuperGrok Heavy。",
        "refreshTokenHowTo": "在本机运行 xAI Grok CLI 登录（loopback OAuth）拿到 refresh_token 后粘贴到这里。xAI 公共 client 无服务端 redirect / device-code 流程，故 token 须在本机铸取。",
        "baseUrl": "Base URL（可选）",
        "baseUrlHint": "默认 https://api.x.ai/v1，仅自建反代时覆盖。",
        "relayBaseUrlHint": "使用 edge 网关地址，例如 https://api-us4.tokenkey.dev。",
        "relayApiKeyHint": "填写该 relay stub 访问 edge 的 TokenKey API key。",
        "tokenEditHint": "留空表示保留当前值",
        "heavyNote": "xAI 把 OAuth API 面限定给 SuperGrok Heavy；标准 / 过期订阅会在请求时返回 HTTP 403。",
        "pleaseEnterRefreshToken": "请输入 Grok refresh token"
      },
      "gemini": {
        "accountType": {
          "vertexTitle": "Gemini Vertex",
          "vertexDesc": "文本 Chat 原生 Vertex"
        }
      },
      "loadModelsUnavailable": "无法加载该账号的模型——账号当前不可用（可能已被删除或正在重新授权）。请刷新账号列表后重试。",
      "loadModelsAuthExpired": "无法加载模型——管理会话已过期，请重新登录后重试。",
      "loadModelsFailed": "加载该账号的模型失败，请重试。"
    },
    "ops": {
      "clientFaults": "客户端过错：",
      "failoverHopStats": {
        "title": "Failover 跳数统计（按账号）",
        "hint": "每个恢复成功的请求绕了几跳 = 浪费的上游往返；用于观测 #899 窗口调度的减跳效果。",
        "failedToLoad": "加载 Failover 跳数统计失败",
        "empty": "当前筛选条件下暂无 failover 跳数数据",
        "totalAccounts": "账号总数：{total}",
        "table": {
          "account": "账号",
          "platform": "平台",
          "recoveredCount": "恢复成功请求数",
          "totalFailoverHops": "Failover 跳数合计",
          "totalWastedAttempts": "浪费上游尝试合计",
          "avgHopsPerRecovered": "每成功请求平均跳数"
        }
      },
      "errorDetail": {
        "apiKey": "API Key",
        "clientIp": "客户端 IP"
      },
      "requestDetails": {
        "table": {
          "requester": "请求方"
        },
        "requester": {
          "anonymous": "匿名",
          "key": "Key",
          "group": "分组",
          "account": "上游账号"
        }
      },
      "alertRules": {
        "metrics": {
          "poolLoadRate": "账号池负载率 (%)",
          "routingCapacityRejectionCount": "无可用账号拒绝数"
        },
        "metricDescriptions": {
          "poolLoadRate": "调度池并发负载率：(在途+排队)/总席位。按平台/分组/渠道分池取最饱和值，≥100% 表示已排队。容量触顶前瞻信号，建议 ≥90% 触发补号。",
          "routingCapacityRejectionCount": "统计窗口内\"无可用账号\"路由拒绝累计次数（空池快速失败 429 + 镜像 edge 下游容量拒绝，error_phase=routing）。这类客户端可见 429 被排除在错误率/成功率之外、也不冷却账号，是唯一能看见薄池瞬时抢空拒绝风暴的信号；为绝对计数（非比率），建议 5 分钟内 ≥50 触发补号/扩容。"
        }
      },
      "email": {
        "feishuTitle": "飞书群 P0 告警",
        "feishuP0OnlyHint": "仅发送 P0 firing 事件；不发送 P1/P2/P3，不发送恢复通知，不会 @所有人。",
        "feishuWebhook": "飞书 Bot Webhook",
        "feishuWebhookHint": "仅支持 HTTPS webhook；保存后不会回显。",
        "feishuWebhookKeepPlaceholder": "已配置，留空保留",
        "feishuWebhookConfiguredHint": "已配置，留空保留；输入新值会覆盖。",
        "feishuSigningSecret": "签名密钥",
        "feishuSecretOptional": "可选",
        "feishuSecretKeepPlaceholder": "已配置，留空保留",
        "feishuSecretConfiguredHint": "已配置，留空保留；输入新值会覆盖。",
        "feishuCooldownSeconds": "冷却时间（秒）",
        "feishuUpstreamBalanceLowThreshold": "上游余额告警阈值（元）",
        "feishuUpstreamBalanceLowThresholdHint": "后台定时拉有公开余额接口的上游渠道账号（当前 DeepSeek）余额，低于此值提前发飞书预警，避免归零断供。需启用上方飞书告警。",
        "configured": "已配置",
        "notConfigured": "未配置",
        "validation": {
          "feishuWebhookRequired": "启用飞书 P0 告警时必须配置 Webhook",
          "feishuWebhookHttps": "飞书 Webhook 必须是 HTTPS URL",
          "feishuRateLimitRange": "飞书每小时限额必须在 1 到 24 之间",
          "feishuCooldownRange": "飞书冷却时间必须在 60 到 86400 秒之间",
          "feishuUpstreamBalanceLowThresholdRange": "上游余额告警阈值必须在 1 到 1000000 之间"
        }
      },
      "settings": {
        "feishuEditHint": "请在“邮件通知配置”卡片中编辑飞书 Webhook、签名密钥、限额和冷却时间。"
      }
    },
    "settings": {
      "coldStart": {
        "title": "新用户冷启动",
        "description": "降低首次使用门槛：可选注册赠额（美元）、自动签发 trial API Key、对外公开模型与价格目录。详见 docs/approved/user-cold-start.md。",
        "signupBonusEnabled": "注册赠额",
        "signupBonusEnabledHint": "为每位新注册用户发放小额美元余额，方便其立即体验付费模型。",
        "signupBonusBalance": "赠额余额（美元）",
        "signupBonusBalanceHint": "注册成功后一次性发放，默认 $1.00。必须 ≥ 0；填 0 表示开启开关但不真正发放。",
        "autoGenerateDefaultToken": "自动签发 Trial API Key",
        "autoGenerateDefaultTokenHint": "注册成功后自动创建一把 API Key，用户无需先打开 Keys 页即可调用网关。",
        "autoGenerateDefaultTokenName": "Trial Key 名称",
        "autoGenerateDefaultTokenNameHint": "自动签发 Key 的展示名称，默认为 \"trial\"。",
        "pricingCatalogPublic": "公开价格目录",
        "pricingCatalogPublicHint": "开启后暴露 GET /api/v1/public/pricing 与 /pricing 路由；关闭后接口返回 404，首页入口隐藏。"
      },
      "gatewayForwarding": {
        "stickyRouting": "Prompt Cache 粘性路由",
        "stickyRoutingHint": "默认开启：网关派生稳定的 prompt_cache_key / metadata.user_id / X-Session-Id 注入到上游，以提高 prompt cache 命中率。关闭后所有分组退化为透传客户端已发送的字段，不再派生。详见 docs/approved/sticky-routing.md。",
        "anthropicRequestNormalize": "Anthropic 请求归一化",
        "anthropicRequestNormalizeHint": "默认开启，在转发前修复客户端的两类常见错误：(1) tool_choice 为 OpenAI 风格字符串（\"auto\" / \"required\" / \"none\"）时改写成 Anthropic 必需的 object 形态；(2) 同时启用 thinking 且 tool_choice 强制工具使用（any / tool）时，删除 thinking 以保留客户端的强制工具使用意图。未知的 tool_choice 字符串保留原样，让上游继续暴露客户端 bug。",
        "openaiAllowClaudeCodeCodexPlugin": "允许在 Claude Code 中使用 Codex 插件",
        "openaiAllowClaudeCodeCodexPluginDesc": "全局开关，仅对已开启「仅允许 Codex 官方客户端」的 OpenAI OAuth 账号生效。开启后，所有此类账号都额外放行通过 Claude Code 的 Codex 插件发起的请求（精确匹配 originator=Claude Code），无需逐账号配置；上游请求仍保持透传。"
      }
    },
    "tierTemplates": {
      "title": "Tier 模板",
      "description": "Anthropic OAuth 稳定性档位（l1..l5）。账号按 id 引用 tier，per-tier 参数运行时解析。",
      "projectionBanner": "tier 是 git baseline 的投影。此处编辑会立即生效并 fan-out 到引用账号，但 ops/anthropic 流水线下次运行会从 git 重断言这些行——UI 编辑仅用于应急/本地变更。",
      "createTier": "创建 Tier",
      "editTier": "编辑 Tier",
      "deleteTier": "删除 Tier",
      "noTiers": "暂无 Tier",
      "columns": {
        "name": "名称",
        "concurrency": "并发",
        "baseRpm": "基础 RPM",
        "maxSessions": "最大会话",
        "tlsProfile": "TLS 模板",
        "actions": "操作"
      },
      "form": {
        "name": "名称",
        "description": "描述",
        "concurrency": "并发",
        "priority": "优先级",
        "priorityHint": "仅投影——accounts.priority 由 window 流水线写入",
        "rateMultiplier": "速率倍数",
        "baseRpm": "基础 RPM",
        "maxSessions": "最大会话",
        "rpmStickyBuffer": "RPM 粘性缓冲",
        "sessionIdleTimeoutMinutes": "会话空闲超时（分钟）",
        "windowCostLimit": "窗口成本上限",
        "windowCostStickyReserve": "窗口成本粘性预留",
        "cacheTtlOverrideEnabled": "缓存 TTL 覆盖",
        "tlsProfileName": "TLS 模板名称",
        "tlsProfileNameHint": "流水线按名 upsert 模板并回填 id",
        "tlsProfileId": "TLS 模板 ID"
      },
      "deleteConfirmMessage": "确定要删除 Tier \"{name}\" 吗？绑定它的账号将回退到其持久化的 extra 值。",
      "createSuccess": "Tier 创建成功",
      "updateSuccess": "Tier 更新成功",
      "deleteSuccess": "Tier 删除成功",
      "loadFailed": "加载 Tier 失败",
      "saveFailed": "保存 Tier 失败",
      "deleteFailed": "删除 Tier 失败"
    }
  },
  "quickstart": {
    "title": "快速开始",
    "subtitle": "选择 API Key 与模型，按客户端复制配置即可开始调用。",
    "selectKey": "选择 API Key",
    "noKeys": "你还没有 API Key，先创建一把再配置客户端。",
    "createKey": "创建 API Key",
    "manageKeys": "管理 Key",
    "viewPricing": "查看定价",
    "tryStudio": "试用 Studio"
  }
}

export default { en, zh }
