import postcss from "postcss";
import presetEnv from "postcss-preset-env";
import selectorParser from "postcss-selector-parser";
import { converter, parse } from "culori";

const rgb = converter("rgb");
const channelName = (name) => `--legacy-rgb${name.slice(1)}`;
const alphaName = (name) => `--legacy-alpha${name.slice(1)}`;

function colorChannels(value) {
  // Relative/incomplete color expressions are not literal theme colors.
  let color;
  try {
    color = rgb(parse(value));
  } catch {
    return null;
  }
  if (!color) return null;
  return {
    channels: [color.r, color.g, color.b]
      .map((v) => Math.round(Math.max(0, Math.min(1, v)) * 255))
      .join(", "),
    alpha: color.alpha ?? 1,
  };
}

// Only the additional bundle is transformed; the normal PostCSS pipeline is untouched.
export async function convertLegacyCss(css) {
  const root = postcss.parse(css);
  const defaults = postcss.rule({ selector: "*, ::before, ::after" });
  root.walkAtRules("property", (rule) => {
    const initial = rule.nodes.find((node) => node.prop === "initial-value");
    defaults.append({ prop: rule.params, value: initial?.value ?? "initial" });
    rule.remove();
  });
  const properties = postcss.atRule({ name: "layer", params: "properties" });
  properties.append(defaults);
  root.prepend(properties);

  root.walkRules((rule) => {
    rule.selector = selectorParser((selectors) => {
      selectors.walkPseudos((pseudo) => {
        // Chrome 83 has neither :where nor :is. Preset Env expands :is.
        if (pseudo.value === ":where") pseudo.value = ":is";
      });
    }).processSync(rule.selector);
  });

  // Preserve dark/theme changes with companion RGB channels, not frozen colors.
  root.walkDecls(/^--/, (decl) => {
    if (decl.prop.startsWith("--legacy-")) return;
    const color = colorChannels(decl.value);
    const alias = /^var\((--[\w-]+)\)$/.exec(decl.value);
    if (color) {
      decl.cloneAfter({ prop: channelName(decl.prop), value: color.channels });
      decl.cloneAfter({ prop: alphaName(decl.prop), value: String(color.alpha) });
    } else if (alias) {
      decl.cloneAfter({ prop: channelName(decl.prop), value: `var(${channelName(alias[1])})` });
      decl.cloneAfter({ prop: alphaName(decl.prop), value: `var(${alphaName(alias[1])}, 1)` });
    }
  });
  root.walkDecls((decl) => {
    decl.value = decl.value.replace(
      /color-mix\(in (?:oklab|srgb),\s*var\((--[\w-]+)\)\s*([\d.]+)%,\s*transparent\)/g,
      (_, name, percent) => `rgba(var(${channelName(name)}), calc(var(${alphaName(name)}, 1) * ${Number(percent) / 100}))`,
    );
    decl.value = decl.value.replace(/([\d.]+)(?:svh|dvh|lvh)\b/g,
      (_, height) => `calc(var(--legacy-viewport-height, 100vh) * ${Number(height) / 100})`);
  });
  // Tailwind guards its modern color overrides. Converted overrides now work without the guard.
  root.walkAtRules("supports", (rule) => {
    if (/^\(color:\s*color-mix\(/.test(rule.params)) rule.replaceWith(...rule.nodes);
  });
  const result = await postcss([presetEnv({
    browsers: "Chrome 83",
    preserve: false,
    features: {
      "custom-properties": false,
      "has-pseudo-class": false,
      "is-pseudo-class": true,
    },
  })]).process(root, { from: undefined });
  result.root.walkDecls(/^(gap|row-gap|column-gap)$/, (decl) => {
    if (decl.parent.type !== "rule") return;
    const selectors = selectorParser().astSync(decl.parent.selector).nodes.map(String);
    const values = decl.value.split(/\s+(?![^()]*\))/);
    const directions = [];
    if (decl.prop !== "row-gap") directions.push(["margin-left", ":not(.flex-col):not(.flex-col-reverse)", values[1] ?? values[0]]);
    if (decl.prop !== "column-gap") directions.push(["margin-top", ".flex-col", values[0]]);
    for (const [prop, direction, value] of directions) {
      const selector = selectors.flatMap((s) => [".flex", ".inline-flex"].map((display) =>
        `html[data-legacy-flex-gap] ${s}${display}:not(.flex-wrap):not(.flex-wrap-reverse):not(.flex-row-reverse)${direction} > * + *`)).join(", ");
      const fallback = postcss.rule({ selector });
      fallback.append({ prop, value });
      decl.parent.after(fallback);
    }
  });
  return result.root.toString();
}
