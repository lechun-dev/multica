import { test } from "node:test";
import assert from "node:assert/strict";
import postcss from "postcss";
import { convertLegacyCss } from "./legacy-css.mjs";

test("layers, nesting, selectors and property defaults work on Chrome 83", async () => {
  const css = await convertLegacyCss(`
    @layer theme, base, utilities;
    @property --tw-border-style { syntax: "*"; inherits: false; initial-value: solid; }
    @layer base { body { margin: 0; } }
    @layer utilities { .card { border-style: var(--tw-border-style); &:where(.wide, .small) { display: flex; } } }
  `);
  assert.doesNotMatch(css, /@layer|@property|:where\(|:is\(|&/);
  assert.match(css, /--tw-border-style: solid/);
  assert.match(css, /\.card\.wide/);
  assert.match(css, /margin: 0/);
});

test("theme channels remain dynamic and alpha colors receive usable fallbacks", async () => {
  const css = await convertLegacyCss(`
    :root { --brand: oklch(0.55 0.16 255); --alias: var(--brand); }
    .dark { --brand: oklch(0.7 0.1 255); }
    .relative-color { --tw-shadow-color: rgb(from red r g b / var(--tw-shadow-alpha)); }
    .tint { color: color-mix(in oklab, var(--alias) 40%, transparent); }
  `);
  assert.doesNotMatch(css, /oklch\(|color-mix\(/);
  assert.match(css, /--legacy-rgb-alias: var\(--legacy-rgb-brand\)/);
  assert.match(css, /rgba\(var\(--legacy-rgb-alias\), calc\(var\(--legacy-alpha-alias, 1\) \* 0.4\)\)/);
  const root = postcss.parse(css);
  const channels = [];
  root.walkDecls("--legacy-rgb-brand", (d) => channels.push(d.value));
  assert.equal(channels.length, 2);
  assert.notEqual(channels[0], channels[1]);
});

test("viewport heights and flex gaps are gated without changing body overflow", async () => {
  const css = await convertLegacyCss(`
    .h-svh { height: 100svh; }
    @media (min-width: 768px) { .md\\:gap-3 { gap: 0.75rem; } }
    body { overflow: hidden; }
  `);
  assert.match(css, /var\(--legacy-viewport-height, 100vh\)/);
  assert.match(css, /html\[data-legacy-flex-gap\].*\.md\\:gap-3.*> \* \+ \*/);
  assert.match(css, /margin-top: 0.75rem/);
  assert.match(css, /overflow: hidden/);
});
