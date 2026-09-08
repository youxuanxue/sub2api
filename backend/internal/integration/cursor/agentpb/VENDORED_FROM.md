# Cursor Agent Protocol

The protocol subset and history reconstruction follow
[can1357/oh-my-pi](https://github.com/can1357/oh-my-pi/tree/a8b0d6cc18a69e90f2bfa0da496bb537fce9319d/packages/ai/src/providers),
commit `a8b0d6cc18a69e90f2bfa0da496bb537fce9319d`, MIT licensed.
See `cursor/proto/agent.proto` and `cursor.ts` in that snapshot.

Wire fields were cross-checked against locally installed Cursor Agent
`2026.09.02-c22c1a3`. This subset includes the current `TurnEndedUpdate` usage,
request context completeness flags, prefetched blobs, and protobuf Value MCP
arguments. No CLI code, SDK runtime, filesystem tools or session cache is shipped.

Regenerate from the backend directory with protoc 33.0 and protoc-gen-go 1.36.10:

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
