---
title: Studio Media Model Coverage Hotfix
status: approved
approved_by: "xuejiao (operator directive in session, 2026-07-02)"
approved_at: "2026-07-02"
authors: [agent]
created: 2026-07-02
related_prs: []
related_commits: []
related_design: docs/approved/pricing-availability-source-of-truth.md, docs/approved/newapi-allow-image-generation-ops.md
---

# Studio Media Model Coverage Hotfix

## Decision

Approve a narrow media-serving hotfix for Studio and TokenKey public model
coverage:

- Replace probe source-pool defaults that depended on mutable group names with
  stable group ids.
- Add verified VolcEngine/Ark media model mappings and pricing for the models
  that returned real prod `200` responses through TokenKey.
- Keep Studio video metadata mechanically aligned with the backend public
  servable video set.

This is a high-risk-approved change because it includes an additive production
SQL migration for `accounts.credentials.model_mapping`.

## Evidence

The new VolcEngine mappings were hot-applied to prod account `7|volcengine|newapi|ct45`
before the migration landed in git, then verified through the prod TokenKey
gateway:

- `doubao-seedream-4-5-251128` -> `200 servable`
- `doubao-seedream-5-0-260128` -> `200 servable`
- `doubao-seedance-1-0-pro-fast-251015` -> `200 servable`

Prod runtime pricing overlay was also synced and verified clean against the git
overlay.

## Non-Goals

- Do not add models that only appeared in an upstream `/models` discovery list
  but failed live serving probes.
- Do not surface `veo-3.1-fast-generate-001`; Vertex group `16` returned
  `429 not_allowlisted`.
- Do not infer OpenAI image availability until an image-scoped OpenAI account
  returns a real `200`.
