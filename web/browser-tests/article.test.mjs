// Browser tests for the built site (web/out). Needs Chrome:
//
//   make web-browser-test        (CHROME=/path/to/chrome to override)
//
// Kept out of scripts/ so `node --test scripts/` stays browser-free.
import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import puppeteer from 'puppeteer-core';

const out = path.join(path.dirname(path.dirname(fileURLToPath(import.meta.url))), 'out');
const types = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.json': 'application/json', '.txt': 'text/plain' };
let server, base, browser;

before(async () => {
  assert.ok(fs.existsSync(out), 'web/out is missing: run make web-build first');
  server = http.createServer((req, res) => {
    let p = path.join(out, decodeURIComponent(new URL(req.url, 'http://x').pathname));
    if (!p.startsWith(out)) { res.writeHead(403).end(); return; }
    if (fs.existsSync(p) && fs.statSync(p).isDirectory()) p = path.join(p, 'index.html');
    if (!fs.existsSync(p)) { res.writeHead(404).end(); return; }
    res.writeHead(200, { 'Content-Type': types[path.extname(p)] ?? 'application/octet-stream' });
    fs.createReadStream(p).pipe(res);
  });
  await new Promise((r) => server.listen(0, '127.0.0.1', r));
  base = `http://127.0.0.1:${server.address().port}`;
  browser = await puppeteer.launch({ executablePath: process.env.CHROME ?? '/usr/bin/google-chrome', args: ['--no-sandbox'] });
});

after(async () => {
  await browser?.close();
  server?.close();
});

async function open(slug, width) {
  const page = await browser.newPage();
  await page.setViewport({ width, height: 900, isMobile: width < 500, hasTouch: width < 500 });
  const errors = [];
  page.on('pageerror', (e) => errors.push(String(e)));
  await page.goto(`${base}/articles/${slug}/`, { waitUntil: 'networkidle0' });
  return { page, errors };
}

const pause = (ms) => new Promise((r) => setTimeout(r, ms));

for (const slug of ['postgres-heap-page', 'postgres-index-page']) {
  test(`${slug}: wide panel follows the section in view, down and back up`, async () => {
    const { page, errors } = await open(slug, 1440);
    const sections = await page.$$eval('section[data-step]', (s) => s.map((x) => ({ id: x.id, step: x.dataset.step })));
    const steps = [...new Set(sections.map((s) => s.step))];
    for (const s of [...sections, ...sections.slice(0, -1).reverse()]) {
      await page.$eval(`#${s.id}`, (el) => window.scrollTo(0, el.getBoundingClientRect().top + window.scrollY - window.innerHeight * 0.4));
      await pause(300);
      const label = await page.$eval('.panel-step', (e) => e.textContent);
      assert.match(label, new RegExp(`^Step ${steps.indexOf(s.step) + 1} of ${steps.length} `), `at #${s.id}`);
    }
    assert.ok(await page.$('.scroll-panel .heap-page'), 'panel renders a snapshot');
    assert.deepEqual(errors, []);
    await page.close();
  });

  for (const width of [390, 768]) {
    test(`${slug}: at ${width}px every step's diagram loads inline, with no sideways page scroll`, async () => {
      const { page, errors } = await open(slug, width);
      assert.equal(await page.$eval('.scroll-panel', (e) => getComputedStyle(e).display), 'none');
      // Re-read the height each step: loaded diagrams make the page taller.
      for (let y = 0; y <= await page.evaluate(() => document.body.scrollHeight); y += 400) {
        await page.evaluate((y) => window.scrollTo(0, y), y);
        await pause(100);
      }
      await page.waitForFunction(() => [...document.querySelectorAll('.inline-diagram')].every((d) => d.querySelector('.heap-page')), { timeout: 10000 })
        .catch(() => {});
      const inline = await page.$$eval('.inline-diagram', (ds) => ds.map((d) => ({ step: d.previousElementSibling?.dataset.step, loaded: !!d.querySelector('.heap-page') })));
      const steps = new Set(await page.$$eval('section[data-step]', (s) => s.map((x) => x.dataset.step)));
      assert.equal(inline.length, steps.size, 'one inline diagram per step');
      assert.deepEqual(inline.filter((d) => !d.loaded).map((d) => d.step), [], 'inline diagrams that never loaded');
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth), width, 'page scrolls sideways');
      assert.deepEqual(errors, []);
      await page.close();
    });
  }
}
