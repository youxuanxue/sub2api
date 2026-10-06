# Cursor Agent Protocol

The protocol subset and history reconstruction follow
[can1357/oh-my-pi](https://github.com/can1357/oh-my-pi/tree/a8b0d6cc18a69e90f2bfa0da496bb537fce9319d/packages/ai/src/providers),
commit `a8b0d6cc18a69e90f2bfa0da496bb537fce9319d`, MIT licensed.
See `cursor/proto/agent.proto` and `cursor.ts` in that snapshot.

Wire fields were cross-checked against locally installed Cursor Agent
`2026.09.02-c22c1a3`. This subset includes the current `TurnEndedUpdate` usage,
request context completeness flags, prefetched blobs, and protobuf Value MCP
arguments. No CLI code, SDK runtime, filesystem tools is shipped. Retained native runs hold only bounded in-memory protocol state.
`InteractionUpdate.token_delta` is field 8; field 6 is the unconsumed user-message
event. Terminal usage fields are optional, so absent buckets must not be treated
as reported zeros. Literal wire regression fixtures preserve these distinctions.

The native Read/Write/Shell/Grep and Pi argument/result subset follows
[oh-my-pi 6d8552d7f9df1852826923f07f0eed4fe29511f3](https://github.com/can1357/oh-my-pi/blob/6d8552d7f9df1852826923f07f0eed4fe29511f3/packages/ai/src/providers/cursor/proto/agent.proto).
These messages only translate calls to caller-declared tools. They do not add
native executors. AGENT mode does not grant gateway execution capability.

Shell timeout semantics were cross-checked against the official distributed CLI
`2026.09.28-64d2043`: `TimeoutBehavior` is UNSPECIFIED=0, CANCEL=1,
BACKGROUND=2. CANCEL maps to a client timeout. For a foreground command, BACKGROUND is
a soft-deadline preference: the client instead completes or reports a timeout
under its own bounded foreground execution. Explicit background execution is
rejected; no process handles or background ownership are accepted by the relay.

Regenerate from the backend directory with protoc 36.2 and protoc-gen-go 1.36.10:

```sh
protoc -I internal/integration/cursor/agentpb --go_out=internal/integration/cursor/agentpb --go_opt=paths=source_relative internal/integration/cursor/agentpb/agent.proto
```

MIT License

Copyright (c) 2025 Mario Zechner
Copyright (c) 2025-2026 Can Bölük
Copyright (c) 2026 Stencil Labs, Inc.

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
