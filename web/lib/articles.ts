// Build-time article loading. Runs only in the Next.js build (server
// components), so a validation error fails `next build`.
import fs from 'node:fs';
import path from 'node:path';
// @ts-expect-error plain JS module shared with node --test and capture
import { parseArticle } from './article-core.mjs';

export type Section = { id: string; heading: string; step: string; startsStep: boolean; html: string };
export type Article = { slug: string; title: string; labActions: string[]; sections: Section[] };

const root = process.cwd();
const articlesDir = path.join(root, 'content/articles');
const snapshotsDir = path.join(root, 'public/snapshots');
// Generated from the Inspector by `make site-actions` (inspector -list-actions).
const actionsFile = path.join(root, '../site/actions.txt');

function knownActions(): string[] {
  if (!fs.existsSync(actionsFile)) {
    throw new Error(`${actionsFile} is missing: run "make site-actions" (or "make web-build") first`);
  }
  return fs.readFileSync(actionsFile, 'utf8').split('\n').map((l) => l.trim()).filter(Boolean);
}

export function slugs(): string[] {
  return fs.readdirSync(articlesDir).filter((f) => f.endsWith('.md')).map((f) => f.slice(0, -3)).sort();
}

export function loadArticle(slug: string): Article {
  const source = fs.readFileSync(path.join(articlesDir, `${slug}.md`), 'utf8');
  return parseArticle(slug, source, {
    actions: knownActions(),
    hasSnapshot: (s: string, id: string) => fs.existsSync(path.join(snapshotsDir, s, `${id}.json`)),
  });
}
