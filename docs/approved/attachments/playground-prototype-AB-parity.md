# Playground Prototype — A↔B Parity Table

> **Why this file exists**
>
> Per `docs/approved/user-cold-start.md` §11.1, the Playground prototype must
> be delivered in two forms: **A** the Vue component
> (`frontend/src/components/playground/PlaygroundPrototype.vue`) and **B** the
> static HTML mockup
> (`docs/approved/attachments/playground-prototype-2026-04-23.html`). The two
> must be visually identical so design decisions made on B are not subverted
> by an A that drifts.
>
> US-032 AC-004 / AC-005 codify that requirement; this file is the human-
> readable side-by-side that lets a reviewer verify it without diff'ing two
> different syntaxes. The Vitest spec
> (`PlaygroundPrototype.spec.ts → "AB parity"`) provides the mechanical gate.

## State enumeration

| State        | Vue `props.state` | HTML `data-state` |
| ------------ | ----------------- | ----------------- |
| 1. empty     | `'empty'`         | `data-state="empty"`     |
| 2. typing    | `'typing'`        | `data-state="typing"`    |
| 3. responded | `'responded'`     | `data-state="responded"` |
| 4. error     | `'error'`         | `data-state="error"`     |

## Per-state DOM contract (`data-testid` parity)

| State    | Element                | Vue selector                  | HTML selector                | Copy / value                                                                         |
| -------- | ---------------------- | ----------------------------- | ---------------------------- | ------------------------------------------------------------------------------------ |
| all      | Group pill             | `[data-testid="group-pill"]`  | `[data-testid="group-pill"]` | `claude-pool-default`                                                                |
| all      | Model pill             | `[data-testid="model-pill"]`  | `[data-testid="model-pill"]` | `claude-sonnet-4.5`                                                                  |
| all      | Trial balance          | `[data-testid="trial-balance"]` | `[data-testid="trial-balance"]` | `Trial credit: $1.00 USD`                                                          |
| all      | Composer input         | `[data-testid="composer-input"]`| `[data-testid="composer-input"]`| placeholder = `Ask anything — uses your trial API key`                             |
| all      | System prompt hint     | `[data-testid="system-prompt-hint"]` | `[data-testid="system-prompt-hint"]` | `System prompt — coming in v2`                                              |
| empty    | Placeholder body       | `[data-testid="placeholder"]` | `[data-testid="placeholder"]`| copy = `Start a conversation to see how your trial key performs.` + `Up to 50 turns · 4096 max tokens · 60s timeout` |
| typing   | User message bubble    | `[data-testid="user-message"]`| `[data-testid="user-message"]`| `Write a haiku about TokenKey.`                                                     |
| typing   | Assistant typing dots  | `[data-testid="assistant-typing"]` | `[data-testid="assistant-typing"]` | 3 bouncing dots + Stop button                                              |
| typing   | Composer disabled      | input + send button `disabled` | input + send button `disabled` | (cannot send while streaming)                                                   |
| typing   | Abort button           | `[data-testid="abort-button"]`| `[data-testid="abort-button"]`| `Stop`                                                                              |
| responded| Assistant message      | `[data-testid="assistant-message"]` | `[data-testid="assistant-message"]` | `Tokens flow softly,\nKeys unlock the model paths,\nQuotas guard the gate.` |
| responded| Usage strip            | `[data-testid="usage-strip"]` | `[data-testid="usage-strip"]`| `input 12 tok` · `output 31 tok` · `est. cost $0.000165`                            |
| error    | Error banner title     | `[data-testid="error-banner"]` (title node) | `[data-testid="error-banner"]` (`.title`) | `Trial balance exhausted`                                  |
| error    | Error banner body      | `[data-testid="error-banner"]` (body node)  | `[data-testid="error-banner"]` (`.body`)  | `Your $1.00 trial credit is used up. Top up via Subscriptions or wait for the next reset.` |
| error    | Send button disabled   | send button `disabled`        | send button `disabled`       | (do not allow re-spam after a failure)                                              |

## Color token parity

A uses Tailwind utility classes that resolve to the same hex values as the
`:root` CSS variables in B:

| Token            | A (Tailwind)        | B (CSS var) | Hex      |
| ---------------- | ------------------- | ----------- | -------- |
| Surface          | `bg-white`          | `--surface` | `#ffffff`|
| Border           | `border-gray-200`   | `--border`  | `#e5e7eb`|
| User bubble bg   | `bg-blue-600`       | `--user-bg` | `#2563eb`|
| Assistant bubble | `bg-gray-100`       | `--assistant-bg` | `#f3f4f6`|
| Error bg         | `bg-red-50`         | `--error-bg`| `#fef2f2`|
| Error border     | `border-red-200`    | `--error-border` | `#fecaca`|
| Primary text     | `text-blue-600`     | `--primary` | `#2563eb`|
| Muted text       | `text-gray-500`     | `--muted`   | `#6b7280`|

## Mechanical gate

`PlaygroundPrototype.spec.ts → it("AB parity: each Vue state has matching
HTML data-state")` reads the HTML file at runtime and asserts:

1. Every `PlaygroundState` value in A appears as a `data-state="..."` in B.
2. Every `data-state="..."` in B is in the `PlaygroundState` enum (no
   orphan states only the HTML knows about).

If you add a new state (e.g. `'streaming-tool-use'`) you must add it on
both sides + extend this table; otherwise the test breaks.
