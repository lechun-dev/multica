/** ES5 bootstrap: it must run even when the application's modern JS cannot parse. */
export const LEGACY_CSS_BOOTSTRAP = `(function () {
  var root = document.documentElement;
  if (root.hasAttribute('data-legacy-css')) return;
  var supports = window.CSS && window.CSS.supports;
  var style = document.createElement('style');
  var layers = false;
  try {
    style.textContent = '@layer multica_probe { html { --multica-layer-probe: 1; } }';
    document.head.appendChild(style);
    layers = window.getComputedStyle(root).getPropertyValue('--multica-layer-probe').trim() === '1';
  } catch (error) {} finally {
    if (style.parentNode) style.parentNode.removeChild(style);
  }
  if (layers && supports && supports('height', '100svh') &&
      supports('color', 'oklch(0.5 0.1 20)') &&
      supports('color', 'color-mix(in oklab, black, white)') &&
      supports('selector(:where(html))') && supports('selector(:has(body))')) return;
  root.setAttribute('data-legacy-css', '');
  var link = document.createElement('link');
  link.id = 'multica-legacy-css';
  link.rel = 'stylesheet';
  link.href = '/compat/legacy.css';
  document.head.appendChild(link);
  function updateHeight() {
    root.style.setProperty('--legacy-viewport-height', window.innerHeight + 'px');
  }
  updateHeight();
  window.addEventListener('resize', updateHeight);
  window.addEventListener('orientationchange', updateHeight);
  function checkGap() {
    var probe = document.createElement('div');
    probe.style.cssText = 'position:absolute;visibility:hidden;display:flex;flex-direction:column;row-gap:1px;';
    var first = document.createElement('div');
    first.style.height = '1px';
    var second = first.cloneNode();
    probe.appendChild(first);
    probe.appendChild(second);
    document.body.appendChild(probe);
    if (probe.scrollHeight !== 3) root.setAttribute('data-legacy-flex-gap', '');
    document.body.removeChild(probe);
  }
  if (document.body) checkGap();
  else document.addEventListener('DOMContentLoaded', checkGap, { once: true });
}());`;
