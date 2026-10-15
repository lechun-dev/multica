import { readFile, mkdir, writeFile } from "node:fs/promises";
import { fileURLToPath, URL } from "node:url";
import console from "node:console";
import path from "node:path";
import postcss from "postcss";
import tailwind from "@tailwindcss/postcss";
import { convertLegacyCss } from "./legacy-css.mjs";

const webRoot = fileURLToPath(new URL("../", import.meta.url));
const source = path.join(webRoot, "app/globals.css");
const compiled = await postcss([tailwind({ base: webRoot, optimize: false })])
  .process(await readFile(source, "utf8"), { from: source });
// Preserve the login module's unlayered priority without changing the shared component.
const loginModule = await readFile(path.join(webRoot,
  "../../packages/views/auth/dingtalk-first-login-frame.module.css"), "utf8");
const login = loginModule.replace(/\.frame\s*:global\((.*)\)/g, "html[data-legacy-css] body $1");
const patches = await readFile(path.join(webRoot, "styles/legacy-patches.css"), "utf8");
const css = await convertLegacyCss(`${compiled.css}\n${login}\n${patches}`);
const output = path.join(webRoot, "public/compat/legacy.css");
await mkdir(path.dirname(output), { recursive: true });
await writeFile(output, css);
console.log(`Legacy CSS generated (${Math.round(css.length / 1024)} KiB). Normal CSS unchanged.`);
