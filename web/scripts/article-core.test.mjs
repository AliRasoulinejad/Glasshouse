import { test } from 'node:test';
import assert from 'node:assert/strict';
import { parseArticle, parseActionSpec } from '../lib/article-core.mjs';

const actions = ['insert_rows', 'vacuum_full'];
const src = (front, body = '## One\n\ntext\n\n## Two\n\nmore\n\n## Three\n\nend\n') =>
  `---\n${front}\n---\n${body}`;

const good = `title: T
actions: [insert_rows]
steps:
  - { id: fresh, section: One, actions: [] }
  - { id: inserted, section: Three, actions: ["insert_rows*3"] }`;

test('sections inherit the step above them', () => {
  const a = parseArticle('x', src(good), { actions });
  assert.deepEqual(a.sections.map((s) => [s.id, s.step, s.startsStep]), [
    ['one', 'fresh', true], ['two', 'fresh', false], ['three', 'inserted', true],
  ]);
  assert.deepEqual(a.steps[1].actions, [{ name: 'insert_rows', times: 3 }]);
});

test('refuses unknown actions in steps and in the lab list', () => {
  assert.throws(() => parseArticle('x', src(good.replace('insert_rows*3', 'drop_table')), { actions }), /unknown action "drop_table"/);
  assert.throws(() => parseArticle('x', src(good.replace('[insert_rows]', '[rm_rf]')), { actions }), /unknown lab action/);
});

test('refuses a step with no captured snapshot', () => {
  assert.throws(
    () => parseArticle('x', src(good), { actions, hasSnapshot: (_, id) => id !== 'inserted' }),
    /step "inserted" has no captured snapshot/,
  );
});

test('refuses missing heading, out-of-order steps, and a first section with no step', () => {
  assert.throws(() => parseArticle('x', src(good.replace('section: Three', 'section: Nope')), { actions }), /not a "## " heading/);
  const swapped = good.replace('section: One', 'section: TMP').replace('section: Three', 'section: One').replace('section: TMP', 'section: Three');
  assert.throws(() => parseArticle('x', src(swapped), { actions }), /out of section order/);
  assert.throws(() => parseArticle('x', src(good.replace('section: One', 'section: Two')), { actions }), /first section must start a step/);
});

test('action spec bounds', () => {
  assert.deepEqual(parseActionSpec('vacuum_full'), { name: 'vacuum_full', times: 1 });
  assert.throws(() => parseActionSpec('insert_rows*0'));
  assert.throws(() => parseActionSpec('insert_rows*51'));
  assert.throws(() => parseActionSpec('insert_rows; drop'));
});
