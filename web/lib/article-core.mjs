// Parses and validates one article. Plain JS so `node --test` and the
// capture script share it with the Next.js build without a TS step.
//
// An article is Markdown with front matter:
//
//   title: Reading a heap page
//   actions: [insert_rows, vacuum_full]     # buttons in "Try it yourself"
//   steps:                                  # diagram states, in order
//     - id: fresh
//       section: What you will see          # the "## " heading it starts at
//       actions: []
//     - id: inserted
//       section: Insert rows
//       actions: [insert_rows*3]            # run 3 times, after the previous step
//
// Steps are cumulative: capture runs each step's actions on the state the
// previous step left. A section with no step of its own keeps the state of
// the step above it.
import { marked } from 'marked';
import { parse as parseYaml } from 'yaml';

const STEP_ID = /^[a-z0-9-]+$/;
const ACTION_SPEC = /^([a-z_]+)(?:\*(\d+))?$/;

export function slugify(text) {
  return text.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '');
}

export function parseActionSpec(spec) {
  const m = ACTION_SPEC.exec(String(spec));
  if (!m) throw new Error(`bad action "${spec}": want name or name*N`);
  const times = m[2] ? Number(m[2]) : 1;
  if (times < 1 || times > 50) throw new Error(`bad action "${spec}": N must be 1..50`);
  return { name: m[1], times };
}

export function splitFrontMatter(source) {
  const m = /^---\n([\s\S]*?)\n---\n/.exec(source);
  if (!m) throw new Error('missing front matter');
  return { meta: parseYaml(m[1]) ?? {}, body: source.slice(m[0].length) };
}

// parseArticle throws on anything the build must refuse: an unknown action,
// a step pointing at a missing heading, steps out of section order, or a
// step with no captured snapshot (when hasSnapshot is given).
export function parseArticle(slug, source, { actions, hasSnapshot }) {
  const { meta, body } = splitFrontMatter(source);
  const fail = (msg) => { throw new Error(`article ${slug}: ${msg}`); };
  if (!meta.title) fail('front matter needs a title');

  const known = new Set(actions);
  const labActions = meta.actions ?? [];
  for (const a of labActions) {
    if (!known.has(a)) fail(`unknown lab action "${a}"`);
  }

  // Group tokens into sections at each "## " heading; anything before the
  // first one belongs to the first section.
  const sections = [];
  let pending = [];
  for (const tok of marked.lexer(body)) {
    if (tok.type === 'heading' && tok.depth === 2) {
      sections.push({ heading: tok.text, id: slugify(tok.text), tokens: [...pending, tok] });
      pending = [];
    } else if (sections.length === 0) {
      pending.push(tok);
    } else {
      sections[sections.length - 1].tokens.push(tok);
    }
  }
  if (sections.length === 0) fail('needs at least one "## " section');

  const stepAt = new Map();
  const steps = [];
  const seen = new Set();
  let lastIndex = -1;
  for (const raw of meta.steps ?? []) {
    const id = String(raw.id ?? '');
    if (!STEP_ID.test(id)) fail(`bad step id "${id}"`);
    if (seen.has(id)) fail(`duplicate step id "${id}"`);
    seen.add(id);
    const index = sections.findIndex((s) => s.heading === raw.section);
    if (index < 0) fail(`step "${id}" names section "${raw.section}", which is not a "## " heading`);
    if (index <= lastIndex) fail(`step "${id}" is out of section order`);
    lastIndex = index;
    const stepActions = (raw.actions ?? []).map(parseActionSpec);
    for (const a of stepActions) {
      if (!known.has(a.name)) fail(`step "${id}" uses unknown action "${a.name}"`);
    }
    if (hasSnapshot && !hasSnapshot(slug, id)) {
      fail(`step "${id}" has no captured snapshot (run make web-capture)`);
    }
    steps.push({ id, actions: stepActions });
    stepAt.set(index, id);
  }
  if (steps.length === 0) fail('needs at least one step');
  if (!stepAt.has(0)) fail('the first section must start a step');

  let current = null;
  return {
    slug,
    title: String(meta.title),
    labActions,
    steps,
    sections: sections.map((s, i) => {
      const starts = stepAt.has(i);
      if (starts) current = stepAt.get(i);
      return { id: s.id, heading: s.heading, step: current, startsStep: starts, html: marked.parser(s.tokens) };
    }),
  };
}
