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

test("server-renders the Ozone landing page", async () => {
  const response = await render();
  assert.equal(response.status, 200);
  assert.match(response.headers.get("content-type") ?? "", /^text\/html\b/i);

  const html = await response.text();
  assert.match(html, /<title>Ozone — Your game, ranked\.<\/title>/i);
  assert.match(html, /Your game,/);
  assert.match(html, /ranked\./);
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
    ["/tournaments", /<title>Tournaments — Ozone<\/title>/, /Tournaments are unavailable right now/],
    ["/rankings", /<title>Rankings — Ozone<\/title>/, /Rankings are unavailable right now/],
    ["/rankings?country=KE", /<title>Rankings — Ozone<\/title>/, /aria-current="page"[^>]*>Kenya</],
  ]) {
    const response = await render(path);
    assert.equal(response.status, 200, path);
    const html = await response.text();
    assert.match(html, title, path);
    assert.match(html, notice, path);
    assert.match(html, /href="\/rankings"/, path);
  }
});

test("server-renders tournament and player pages, and a 404 for anything else", async () => {
  // No API runs under the test, so a well-formed link degrades to its notice.
  for (const [path, title, notice] of [
    ["/tournaments/11e556a9-8ec6-451c-98be-0096d40d1ce1", /<title>Tournament — Ozone<\/title>/, /This tournament is unavailable right now/],
    ["/players/11e556a9-8ec6-451c-98be-0096d40d1ce1", /<title>Player — Ozone<\/title>/, /This player is unavailable right now/],
  ]) {
    const response = await render(path);
    assert.equal(response.status, 200, path);
    const html = await response.text();
    assert.match(html, title, path);
    assert.match(html, notice, path);
  }
  for (const path of ["/tournaments/not-a-tournament", "/players/nobody"]) {
    const response = await render(path);
    assert.equal(response.status, 404, path);
    assert.match(await response.text(), /That page doesn&#x27;t exist|That page doesn’t exist|That page doesn't exist/, path);
  }
});

test("server-renders the legal pages and links them with the socials from every footer", async () => {
  for (const [path, title, heading] of [
    ["/terms", "Terms of Service", /Fair play and conduct/],
    ["/privacy", "Privacy Policy", /Reading screenshots/],
    ["/refunds", "Refund Policy", /When you get a full refund/],
    ["/fair-play", "Fair Play Rules", /Conduct strikes/],
  ]) {
    const response = await render(path);
    assert.equal(response.status, 200, path);
    const html = await response.text();
    assert.match(html, new RegExp(`<title>${title} — Ozone</title>`), path);
    assert.match(html, heading, path);
    assert.match(html, /Last updated/, path);
  }

  const html = await (await render("/")).text();
  for (const href of ["/terms", "/privacy", "/refunds", "/fair-play"]) {
    assert.match(html, new RegExp(`href="${href}"`), href);
  }
  for (const network of ["TikTok", "Instagram", "YouTube", "X"]) {
    assert.match(html, new RegExp(`aria-label="Ozone on ${network}"`), network);
  }
  assert.match(html, /rel="noopener noreferrer"/);
});

test("removes starter-only preview infrastructure", async () => {
  const [page, layout, packageJson] = await Promise.all([
    readFile(new URL("../app/page.tsx", import.meta.url), "utf8"),
    readFile(new URL("../app/layout.tsx", import.meta.url), "utf8"),
    readFile(new URL("../package.json", import.meta.url), "utf8"),
  ]);

  assert.doesNotMatch(page, /Nairobi Sunday Knockout/);
  assert.match(layout, /Ozone — Your game, ranked\./);
  assert.doesNotMatch(packageJson, /react-loading-skeleton/);
  await assert.rejects(access(new URL("../app/_sites-preview/SkeletonPreview.tsx", import.meta.url)));
  await assert.rejects(access(new URL("../app/_sites-preview/preview.css", import.meta.url)));
});
