import test from 'node:test';
import assert from 'node:assert/strict';
import { createCatalogValidator } from './catalog.tk.mjs';

test('model and complete variant are verified per credential; catalog expires', async () => {
  let calls = 0;
  let now = 0;
  const validator = createCatalogValidator({ models: { async list({ apiKey }) {
    calls++;
    return [{ id: apiKey, variants: [{ params: [{ id: 'fast', value: 'false' }] }] }];
  } } }, { now: () => now, ttlMs: 10 });
  const body = { model: 'composer', cursor_model: { id: 'composer', params: [{ id: 'fast', value: 'false' }] } };
  await validator(body, 'composer');
  await validator(body, 'composer');
  assert.equal(calls, 1);
  await assert.rejects(validator(body, 'other'), { status: 400 });
  await assert.rejects(validator({ ...body, cursor_model: { id: 'composer', params: [] } }, 'composer'), { status: 400 });
  await assert.rejects(validator({ model: 'default', cursor_model: { id: 'default', params: [] } }, 'composer'), { status: 400 });
  now = 11;
  await validator(body, 'composer');
  assert.equal(calls, 3);
});
