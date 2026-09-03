#!/usr/bin/env node
// 2026-09-03 coder(lq): Generate the committed preview icon set from the
// stable source icon. Keeping this script in the repository makes the Beta
// branding reproducible on a fresh checkout and avoids runner-specific tools.

import { execFileSync } from "node:child_process";
import { mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import sharp from "sharp";

const desktopRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const source = join(desktopRoot, "build/icon.png");
const output = join(desktopRoot, "build-beta");
const sizes = [16, 24, 32, 48, 64, 128, 256, 512];

mkdirSync(join(output, "icons"), { recursive: true });

const badge = Buffer.from(`
<svg width="330" height="180" xmlns="http://www.w3.org/2000/svg">
  <polygon points="55,0 330,0 330,180 0,180" fill="#d94841"/>
  <text x="184" y="113" fill="white" font-family="Arial, sans-serif"
    font-size="54" font-weight="700" text-anchor="middle" letter-spacing="2">BETA</text>
</svg>`);

// Render the badge before resizing. Sharp applies queued operations in a
// single pipeline, so resizing after `composite()` would otherwise make the
// 330×180 badge larger than a 16×16 target and fail the small-icon builds.
const compositedIcon = await sharp(source)
  .composite([{ input: badge, gravity: "northeast" }])
  .png()
  .toBuffer();

await writeFileSync(join(output, "icon.png"), compositedIcon);
for (const size of sizes) {
  await sharp(compositedIcon)
    .resize(size, size)
    .toFile(join(output, "icons", `${size}x${size}.png`));
}

// ICO files may contain PNG payloads. Build a standards-compliant multi-size
// container without requiring ImageMagick on GitHub-hosted Windows runners.
const pngs = await Promise.all(
  [16, 24, 32, 48, 64, 128, 256].map(async (size) => ({
    size,
    data: readFileSync(join(output, "icons", `${size}x${size}.png`)),
  })),
);
const headerSize = 6 + pngs.length * 16;
let offset = headerSize;
const header = Buffer.alloc(headerSize);
header.writeUInt16LE(0, 0);
header.writeUInt16LE(1, 2);
header.writeUInt16LE(pngs.length, 4);
for (let index = 0; index < pngs.length; index += 1) {
  const { size, data } = pngs[index];
  const entry = 6 + index * 16;
  header.writeUInt8(size === 256 ? 0 : size, entry);
  header.writeUInt8(size === 256 ? 0 : size, entry + 1);
  header.writeUInt8(0, entry + 2);
  header.writeUInt8(0, entry + 3);
  header.writeUInt16LE(1, entry + 4);
  header.writeUInt16LE(32, entry + 6);
  header.writeUInt32LE(data.length, entry + 8);
  header.writeUInt32LE(offset, entry + 12);
  offset += data.length;
}
writeFileSync(join(output, "icon.ico"), Buffer.concat([header, ...pngs.map((x) => x.data)]));

// iconutil is available on macOS runners and emits the native ICNS container
// from the same PNG set. Other platforms keep the already committed ICNS.
if (process.platform === "darwin") {
  const iconset = join(output, "icon.iconset");
  mkdirSync(iconset, { recursive: true });
  const mapping = [
    [16, "icon_16x16.png"],
    [32, "icon_16x16@2x.png"],
    [32, "icon_32x32.png"],
    [64, "icon_32x32@2x.png"],
    [128, "icon_128x128.png"],
    [256, "icon_128x128@2x.png"],
    [256, "icon_256x256.png"],
    [512, "icon_256x256@2x.png"],
    [512, "icon_512x512.png"],
  ];
  for (const [size, name] of mapping) {
    await sharp(compositedIcon)
      .resize(size, size)
      .toFile(join(iconset, name));
  }
  execFileSync("iconutil", ["-c", "icns", iconset, "-o", join(output, "icon.icns")]);
  rmSync(iconset, { recursive: true, force: true });
}
