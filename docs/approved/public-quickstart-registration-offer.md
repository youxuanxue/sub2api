---
title: Public Quickstart and truthful registration offer
status: approved
approved_by: "用户（本会话确认：可以，做到完整、统一、ssot 的体验。继续。）"
approved_at: "2026-09-11"
created: "2026-09-11"
authors: [codex]
---

# Public Quickstart and registration offer

## Contract

`/quickstart` is the single public and authenticated connection guide. Its static
content is public in backend mode too, using an exact route exception. No new docs
route or docs-public setting exists. Protected APIs retain their authorization.
Visitors can select clients and inspect/copy placeholder configuration. No anonymous
key lookup, import, model discovery, warmup, test or creation is performed. Signed-in
users consume existing key/capability owners. Creating a missing key is explicit;
returning from authentication never automatically sends a test request.

Public settings API and HTML injection expose one `registration_offer` projection:
`open`, `invitation_required`, `closed`, `unavailable`. Only open offers may carry
`signup_bonus_usd`. This is current configuration, not reserved eligibility or a
trial-key/first-call guarantee. Actual registration retains verification, invitation,
email-identity, quota and billing owners; no atomic activation redesign is included.
Missing/corrupt configuration or read failure must not advertise registration/credit.
Refresh on entry, visibility restoration, periodically while visible and before
submission; failure invalidates the previous offer. Registration outcome is decided
at submission, not by browser cache. Third-party pending-auth completion is distinct.

All first-party CTAs consume the same offer and component. Client/protocol/transport
selection survives login and both email registration steps through a same-origin
return path containing no credentials. Home (including custom/compact), model/price
and login pages link to `/quickstart`. The default model in a preview is a placeholder,
not an inferred entitlement. Secrets are never placed in return paths or docs storage.

## Owners

| Responsibility | Owner |
| --- | --- |
| Public policy/config projection | `backend/internal/service/registration_offer_tk.go` |
| Offer refresh and consumption | `frontend/src/composables/useRegistrationOffer.tk.ts` |
| Registration CTA | `frontend/src/components/auth/RegistrationActionTk.vue` |
| Return path | `frontend/src/utils/quickstartJourney.tk.ts` |
| Client metadata | existing `frontend/src/constants/clientIntegrations.tk.ts` |
| Shared configuration rendering/generation | existing `frontend/src/components/keys/UseKeyGuide.vue` |
| Page orchestration | existing `frontend/src/views/user/QuickstartView.vue` |

## Validation

Backend tests cover offer states, malformed settings and API/injection parity.
Frontend behavior tests cover no anonymous requests, explicit creation, placeholder
configs, preserved return selection and offer failure. Real Playwright UI exercises
anonymous browsing, registration/login return, no-key creation, closed/unavailable
states, mobile layout and backend-mode route boundaries. Sentinels anchor owners and
call sites. Production settings and rollout are separate from implementation.
