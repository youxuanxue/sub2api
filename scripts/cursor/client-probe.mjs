import { readFileSync, writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { resolve } from 'node:path';
import assert from 'node:assert/strict';

const root = fileURLToPath(new URL('../../', import.meta.url));
const state = resolve(root, '.cache/cursor-dev');
const { key } = JSON.parse(readFileSync(resolve(state, 'gateway-key.json'), 'utf8'));
const { default: OpenAI } = await import(resolve(state, 'clients/node_modules/openai/index.mjs'));
const { default: Anthropic } = await import(resolve(state, 'clients/node_modules/@anthropic-ai/sdk/index.mjs'));
const requests = [];
const config = { apiKey: key, baseURL: 'http://127.0.0.1:18097/v1', maxRetries: 0, timeout: 90_000,
  async fetch(input, init) {
    const response = await fetch(input, init);
    const clientRequestId = response.headers.get('x-client-request-id');
    requests.push({ endpoint: new URL(response.url).pathname, status: response.status, requestId: response.headers.get('x-request-id'), clientRequestId });
    return response;
  }
};
const openai = new OpenAI(config);
const anthropic = new Anthropic({ ...config, baseURL: 'http://127.0.0.1:18097' });
const results = [];
const schema = { type: 'object', properties: { path: { type: 'string', enum: ['fixture.txt'] } }, required: ['path'], additionalProperties: false };
const prompt = 'Call read_fixture with path fixture.txt. Then return the exact file contents without commentary. Do not guess its contents.';
const model = process.env.CURSOR_PROBE_MODEL || 'composer-2.5';
function execute(name, args) { assert.equal(name, 'read_fixture'); assert.equal(args.path, 'fixture.txt'); return 'CURSOR_TOOL_VERIFIED_7291'; }
function lastRequestId() {
  const request = requests.at(-1);
  assert.equal(request?.status, 200);
  assert.ok(request.clientRequestId, 'Missing request correlation');
  return request.clientRequestId;
}
const mode = process.argv[2] || 'tools';
try {
  if (mode === 'text') {
    const response = await openai.chat.completions.create({ model, messages: [{ role: 'user', content: 'Reply exactly OK.' }], max_tokens: 64 });
    assert.match(response.choices[0].message.content.trim(), /^OK[.!]?$/i);
    results.push({ model, protocol: 'chat', status: 'passed', turns: [response.usage], requestIds: [lastRequestId()] });
  } else if (mode === 'parallel') {
    const tools = ['read_left', 'read_right'].map(name => ({ name, description: 'Read one independent local fixture.', input_schema: schema }));
    const messages = [{ role: 'user', content: 'Call read_left and read_right with path fixture.txt together in the same turn before answering. They are independent. Return both results, without commentary.' }];
    const first = await anthropic.messages.create({ model, max_tokens: 256, tools, messages });
    const firstRequestId = lastRequestId();
    const calls = first.content.filter(block => block.type === 'tool_use');
    assert.equal(calls.length, 2);
    assert.deepEqual(calls.map(call => call.name).sort(), ['read_left', 'read_right']);
    messages.push({ role: 'assistant', content: first.content }, { role: 'user', content: calls.map(call => ({ type: 'tool_result', tool_use_id: call.id, content: `${call.name}_VERIFIED_7291` })) });
    const final = await anthropic.messages.create({ model, max_tokens: 256, tools, messages });
    const text = final.content.filter(block => block.type === 'text').map(block => block.text).join('');
    assert.ok(text.includes('read_left_VERIFIED_7291') && text.includes('read_right_VERIFIED_7291'));
    results.push({ model, protocol: 'messages', status: 'passed', tools: calls.map(call => call.name), turns: [first.usage, final.usage], requestIds: [firstRequestId, lastRequestId()], text });
  } else {
    for (const protocol of process.env.CURSOR_PROBE_PROTOCOL ? [process.env.CURSOR_PROBE_PROTOCOL] : ['messages', 'chat', 'responses']) {
      const turns = [];
      const requestIds = [];
      let text;
      if (protocol === 'messages') {
        const tools = [{ name: 'read_fixture', description: 'Read the local verification fixture.', input_schema: schema }];
        const messages = [{ role: 'user', content: prompt }];
        const first = await anthropic.messages.create({ model, max_tokens: 256, tools, messages });
        requestIds.push(lastRequestId());
        const calls = first.content.filter(block => block.type === 'tool_use');
        assert.equal(calls.length, 1);
        messages.push({ role: 'assistant', content: first.content }, { role: 'user', content: calls.map(call => ({ type: 'tool_result', tool_use_id: call.id, content: execute(call.name, call.input) })) });
        const stream = anthropic.messages.stream({ model, max_tokens: 256, tools, messages });
        const final = await stream.finalMessage();
        requestIds.push(lastRequestId());
        writeFileSync(resolve(state, 'evidence/client-messages-final.json'), JSON.stringify(final, null, 2));
        text = final.content.filter(block => block.type === 'text').map(block => block.text).join('');
        turns.push(first.usage, final.usage);
        const replay = await anthropic.messages.create({ model, max_tokens: 256, tools, messages });
        requestIds.push(lastRequestId());
        turns.push(replay.usage);
        assert.ok(replay.content.some(block => block.type === 'text' && block.text.includes('CURSOR_TOOL_VERIFIED_7291')));
      } else if (protocol === 'chat') {
        const tools = [{ type: 'function', function: { name: 'read_fixture', description: 'Read the local verification fixture.', parameters: schema } }];
        const messages = [{ role: 'user', content: prompt }];
        const first = await openai.chat.completions.create({ model, max_tokens: 256, tools, messages });
        requestIds.push(lastRequestId());
        const message = first.choices[0].message;
        writeFileSync(resolve(state, 'evidence/client-chat-tool.json'), JSON.stringify(first, null, 2));
        assert.equal(message.tool_calls?.length, 1);
        messages.push(message, ...message.tool_calls.map(call => ({ role: 'tool', tool_call_id: call.id, content: execute(call.function.name, JSON.parse(call.function.arguments)) })));
        const final = await openai.chat.completions.create({ model, max_tokens: 256, tools, messages });
        requestIds.push(lastRequestId());
        writeFileSync(resolve(state, 'evidence/client-chat-final.json'), JSON.stringify(final, null, 2));
        text = final.choices[0].message.content;
        turns.push(first.usage, final.usage);
      } else {
        const tools = [{ type: 'function', name: 'read_fixture', description: 'Read the local verification fixture.', parameters: schema, strict: true }];
        const input = [{ role: 'user', content: prompt }];
        const first = await openai.responses.create({ model, max_output_tokens: 256, tools, input });
        requestIds.push(lastRequestId());
        const calls = first.output.filter(item => item.type === 'function_call');
        assert.equal(calls.length, 1);
        input.push(...first.output, ...calls.map(call => ({ type: 'function_call_output', call_id: call.call_id, output: execute(call.name, JSON.parse(call.arguments)) })));
        const final = await openai.responses.create({ model, max_output_tokens: 256, tools, input });
        requestIds.push(lastRequestId());
        writeFileSync(resolve(state, 'evidence/client-responses-final.json'), JSON.stringify(final, null, 2));
        text = final.output_text;
        turns.push(first.usage, final.usage);
      }
      assert.ok(text.includes('CURSOR_TOOL_VERIFIED_7291'), `${protocol} did not return the tool result`);
      results.push({ model, protocol, status: 'passed', turns, requestIds, text });
      console.log(`${protocol}: tool call and continuation passed`);
    }
  }
} catch (error) {
  results.push({ model, status: 'failed', httpStatus: error.status, error: error.message });
  process.exitCode = 1;
} finally {
  writeFileSync(resolve(state, `evidence/client-${mode}.json`), JSON.stringify({ at: new Date().toISOString(), results, requests }, null, 2));
  console.log(JSON.stringify(results));
}
