# Upstream-deferred service tests (B residual)

Restored and green in B after MH symbol grafts:
gemini not-found / compat-thinking / error-policy / vertex* / openai cursor /
model-retirement / oauth web_search / responses created_at / payment confirm.

Still parked (`//go:build ignore`) until TK absorbs the matching MH behavior:

- gemini cache storage billing + surcharge (`gemini_cached_content*_test.go`, hardening)
- gemini image-generation group gate + stream abort billing
- gemini third-party/vertex quota skip
- single-account retry probe metadata semantics
- opencode model-routed account connection probe
- payment refund pending-hold helpers

Do not delete; restore when the owner APIs land without silent TK regression.

Restored during the 2026-11-11 merge: subscription long-context exemption and explicit temporary-unschedulable scope tests now run in the service package.
