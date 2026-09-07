import test from 'node:test';
import assert from 'node:assert/strict';
import { CursorHarnessMessagesBridge, cursorSdkModelSelection } from './upstream/harness_messages.mjs';
import { estimateParkedTurn } from './usage.tk.mjs';

test('text-only runs use the explicit SDK store and delete their state', async () => {
  const deleted = [];
  const store = {
    runs: { async list() { return { items: [{ runId: 'run-1' }] }; }, async delete() { deleted.push('runs'); } },
    runEvents: { async delete() { deleted.push('events'); } },
    checkpoints: { async delete() { deleted.push('checkpoints'); } },
    agents: { async delete({ filter }) { assert.deepEqual(filter.agentIds, ['agent-1']); deleted.push('agents'); } },
  };
  const bridge = new CursorHarnessMessagesBridge({ sdkStore: store,
    agentFactory: async options => {
      assert.equal(options.local.store, store);
      assert.deepEqual(options.tools, []);
      return { agentId: 'agent-1', close() {}, async send() {
        return { status: 'running', async *stream() {
          yield { type: 'assistant', message: { content: [{ type: 'text', text: 'OK' }] } };
          this.status = 'finished';
        }, async wait() { return { status: 'finished' }; }, async cancel() {} };
      } };
    }
  });
  try {
    await bridge.handle({ model: 'composer-2.5', messages: [{ role: 'user', content: 'Hi' }] }, 'key');
    assert.deepEqual(deleted, ['events', 'checkpoints', 'runs', 'agents']);
  } finally { await bridge.shutdown(); }
});

test('missing parked usage is estimated once and reconciled with the final SDK total', async () => {
  const bridge = new CursorHarnessMessagesBridge({ incrementalUsage: true, rejectReplay: true, estimateParkedTurn, parallelCollectMs: 1,
    agentFactory: async options => ({ close() {}, async send() {
      return { status: 'running', async *stream() {
        await options.local.customTools.read.execute({ path: 'fixture.txt' });
        yield { type: 'assistant', message: { content: [{ type: 'text', text: 'OK' }] } };
        this.status = 'finished';
      }, async wait() { return { status: 'finished', usage: { inputTokens: 2000, outputTokens: 400, cacheReadTokens: 200 } }; }, async cancel() {} };
    } })
  });
  const body = { model: 'composer-2.5', messages: [{ role: 'user', content: 'Read fixture.txt' }], tools: [{ name: 'read', input_schema: { type: 'object' } }] };
  try {
    const first = await bridge.handle(body, 'key', { tenantId: 'owner' });
    assert.ok(first.usage.input_tokens > 0);
    assert.ok(first.usage.output_tokens > 0);
    const tool = first.content.find(block => block.type === 'tool_use');
    const final = await bridge.handle({ ...body, messages: [{ role: 'user', content: [{ type: 'tool_result', tool_use_id: tool.id, content: 'OK' }] }] }, 'key', { tenantId: 'owner' });
    assert.equal(first.usage.input_tokens + final.usage.input_tokens, 2000);
    assert.equal(first.usage.output_tokens + final.usage.output_tokens, 400);
    assert.equal(final.usage.cache_read_input_tokens, 200);
  } finally { await bridge.shutdown(); }
});

test('trusted catalog parameters reach the SDK without implicit fast mode', () => {
  const selection = { id: 'composer-2.5', params: [{ id: 'fast', value: 'false' }] };
  assert.deepEqual(cursorSdkModelSelection('composer-2.5', { cursor_model: selection }), selection);
  assert.throws(() => cursorSdkModelSelection('gpt-5.5', { cursor_model: selection }), { status: 400 });
});

test('each HTTP turn bills only new usage and duplicate tool results fail closed', async () => {
  const bridge = new CursorHarnessMessagesBridge({ incrementalUsage: true, rejectReplay: true, parallelCollectMs: 1,
    agentFactory: async options => ({ close() {}, async send() {
      return { status: 'running', async *stream() {
        yield { type: 'usage', usage: { inputTokens: 100, outputTokens: 10, cacheReadTokens: 20 } };
        await options.local.customTools.lookup.execute({ turn: 1 });
        yield { type: 'usage', usage: { inputTokens: 50, outputTokens: 5, cacheReadTokens: 10 } };
        await options.local.customTools.lookup.execute({ turn: 2 });
        yield { type: 'usage', usage: { inputTokens: 40, outputTokens: 4, cacheReadTokens: 5 } };
        yield { type: 'assistant', message: { content: [{ type: 'text', text: 'OK' }] } };
        this.status = 'finished';
      }, async wait() { return { status: 'finished', usage: { inputTokens: 190, outputTokens: 19, cacheReadTokens: 35 } }; }, async cancel() {} };
    } })
  });
  const body = { model: 'composer-2.5', messages: [{ role: 'user', content: 'Look up both turns' }], tools: [{ name: 'lookup', input_schema: { type: 'object' } }] };
  const owner = { tenantId: 'user:1:key:2:account:3' };
  const resume = message => ({ ...body, messages: [{ role: 'user', content: message.content.filter(b => b.type === 'tool_use').map(b => ({ type: 'tool_result', tool_use_id: b.id, content: 'value' })) }] });
  try {
    const first = await bridge.handle(body, 'key', owner);
    assert.equal(first.usage.input_tokens, 100);
    await assert.rejects(bridge.handle(resume(first), 'key', { tenantId: 'user:2:key:3:account:3' }), { status: 409 });
    await assert.rejects(bridge.handle(resume(first), 'other-key', owner), { status: 409 });
    const second = await bridge.handle(resume(first), 'key', owner);
    assert.equal(second.usage.input_tokens, 50);
    await assert.rejects(bridge.handle(resume(first), 'key', owner), { status: 409 });
    const final = await bridge.handle(resume(second), 'key', owner);
    assert.equal(final.usage.input_tokens, 40);
    assert.equal(final.usage.cache_read_input_tokens, 5);
    assert.equal(final.content[0].text, 'OK');
    await assert.rejects(bridge.handle(resume(second), 'key', owner), { status: 409 });
    assert.equal(first.usage.output_tokens + second.usage.output_tokens + final.usage.output_tokens, 19);
  } finally { await bridge.shutdown(); }
});

test('run deadline cancels an SDK run even when no semantic output arrives', async () => {
  let cancelled = false;
  let release;
  const pending = new Promise(resolve => { release = resolve; });
  const bridge = new CursorHarnessMessagesBridge({ maxRunMs: 20, firstEventTimeoutMs: 1000,
    agentFactory: async () => ({ close() {}, async send() {
      return { status: 'running', async *stream() { await pending; }, async wait() { return {}; },
        async cancel() { cancelled = true; this.status = 'cancelled'; release(); } };
    } })
  });
  // Keep the test event loop alive while the production deadline timer is unref'ed.
  const timer = setTimeout(() => {}, 1000);
  try {
    await assert.rejects(bridge.handle({ model: 'composer', messages: [{ role: 'user', content: 'Hi' }] }, 'key'), { status: 504 });
    assert.equal(cancelled, true);
    assert.equal(bridge.status().liveSessions, 0);
  } finally { clearTimeout(timer); await bridge.shutdown(); }
});
