// Captures every article step's snapshot from a real, fresh Postgres.
//
//   node scripts/capture.mjs [slug ...]      (make web-capture)
//
// For each article: restart the local stack on a fresh volume (make
// stack-restart), run each step's actions in order through the Inspector,
// and save GET /snapshot after each step to public/snapshots/<slug>/<step>.json.
// Nothing is drawn or edited by hand.
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { parseArticle } from '../lib/article-core.mjs';

const web = path.dirname(path.dirname(fileURLToPath(import.meta.url)));
const repo = path.dirname(web);
const inspector = process.env.GLASSHOUSE_INSPECTOR_URL ?? 'http://127.0.0.1:8765';

async function waitHealthy() {
  for (let i = 0; i < 60; i++) {
    try {
      const r = await fetch(`${inspector}/health`);
      if (r.ok) return (await r.json()).actions.map((a) => a.name);
    } catch { /* not up yet */ }
    await new Promise((r) => setTimeout(r, 1000));
  }
  throw new Error(`Inspector at ${inspector} never became healthy`);
}

async function runAction(name) {
  const r = await fetch(`${inspector}/actions/${name}`, { method: 'POST', headers: { 'X-Glasshouse-Action': '1' } });
  if (!r.ok) throw new Error(`action ${name}: HTTP ${r.status} ${await r.text()}`);
}

async function snapshot() {
  const r = await fetch(`${inspector}/snapshot`);
  if (!r.ok) throw new Error(`snapshot: HTTP ${r.status}`);
  const env = await r.json();
  return { schema_version: env.schema_version, snapshot: env.snapshot };
}

const dir = path.join(web, 'content/articles');
const wanted = process.argv.slice(2);
const slugs = fs.readdirSync(dir).filter((f) => f.endsWith('.md')).map((f) => f.slice(0, -3))
  .filter((s) => wanted.length === 0 || wanted.includes(s));

for (const slug of slugs) {
  console.log(`== ${slug}: fresh stack`);
  execFileSync('make', ['-C', repo, 'stack-restart'], { stdio: 'inherit' });
  const actions = await waitHealthy();
  const article = parseArticle(slug, fs.readFileSync(path.join(dir, `${slug}.md`), 'utf8'), { actions });
  const out = path.join(web, 'public/snapshots', slug);
  fs.rmSync(out, { recursive: true, force: true });
  fs.mkdirSync(out, { recursive: true });
  for (const step of article.steps) {
    for (const a of step.actions) {
      for (let i = 0; i < a.times; i++) await runAction(a.name);
    }
    fs.writeFileSync(path.join(out, `${step.id}.json`), JSON.stringify(await snapshot(), null, 1) + '\n');
    console.log(`   ${step.id}: ${step.actions.map((a) => `${a.name}×${a.times}`).join(', ') || '(fresh)'}`);
  }
}
