import assert from "node:assert/strict";
import { access, readFile } from "node:fs/promises";
import test from "node:test";

async function render(path = "/") {
  const workerUrl = new URL("../dist/server/index.js", import.meta.url);
  workerUrl.searchParams.set("test", `${process.pid}-${Date.now()}`);
  const { default: worker } = await import(workerUrl.href);

  return worker.fetch(
    new Request(`http://localhost${path}`, { headers: { accept: "text/html" } }),
    { ASSETS: { fetch: async () => new Response("Not found", { status: 404 }) } },
    { waitUntil() {}, passThroughOnException() {} },
  );
}

test("server-renders the Tonits landing page", async () => {
  const response = await render();
  assert.equal(response.status, 200);
  assert.match(response.headers.get("content-type") ?? "", /^text\/html\b/i);

  const html = await response.text();
  assert.match(html, /<title>Tonits — Your game\. Your name\.<\/title>/i);
  assert.match(html, /Your game\./);
  assert.match(html, /Your name\./);
  assert.match(html, /How it works/);
  assert.match(html, /One of you submits the score\. The other confirms it\./);
  assert.match(html, /\/images\/player-left\.webp/);
  assert.match(html, /\/images\/player-right\.webp/);
  assert.doesNotMatch(html, /Gamics|Nairobi Sunday Knockout|Run your own league/);
  assert.doesNotMatch(html, /codex-preview|Your site is taking shape|react-loading-skeleton/i);
});

test("server-renders the public tournament and ranking pages", async () => {
  // No API runs under the test, so both pages must degrade to their notices.
  for (const [path, title, notice] of [
    ["/tournaments", /<title>Tournaments — Tonits<\/title>/, /Tournaments are unavailable right now/],
    ["/rankings", /<title>Rankings — Tonits<\/title>/, /Rankings are unavailable right now/],
    ["/rankings?country=KE", /<title>Rankings — Tonits<\/title>/, /aria-current="page"[^>]*>Kenya</],
  ]) {
    const response = await render(path);
    assert.equal(response.status, 200, path);
    const html = await response.text();
    assert.match(html, title, path);
    assert.match(html, notice, path);
    assert.match(html, /href="\/rankings"/, path);
  }
});

test("removes starter-only preview infrastructure", async () => {
  const [page, layout, packageJson] = await Promise.all([
    readFile(new URL("../app/page.tsx", import.meta.url), "utf8"),
    readFile(new URL("../app/layout.tsx", import.meta.url), "utf8"),
    readFile(new URL("../package.json", import.meta.url), "utf8"),
  ]);

  assert.doesNotMatch(page, /Nairobi Sunday Knockout/);
  assert.match(layout, /Tonits — Your game\. Your name\./);
  assert.doesNotMatch(packageJson, /react-loading-skeleton/);
  await assert.rejects(access(new URL("../app/_sites-preview/SkeletonPreview.tsx", import.meta.url)));
  await assert.rejects(access(new URL("../app/_sites-preview/preview.css", import.meta.url)));
});
