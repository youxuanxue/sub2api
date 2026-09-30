# Cursor Fable Model Display

## Background

The live Cursor account mapping serves `claude-fable-5` and `claude-fable-5-1`, and does not serve `claude-opus-5-5`. Both Fable models are also served through Tokensea and CloudWise.

## Delta

- MODIFIED: Cursor account mapping SSOT matches the live mapping: retain the two Fable identities and omit `claude-opus-5-5`.
- ADDED: Display both Fable models in the public model catalog because they have serving paths.

## Scenarios

- Cursor account mapping preset contains both Fable IDs and excludes Opus 5.5.
- Cursor display projection contains both Fable IDs.

## Validation

- `TestCursorMappingFloorExcludesGPTWithoutChangingOtherProviders` covers mapping and display projections.
- The generated model-surface bundle is checked against its Go owner.
