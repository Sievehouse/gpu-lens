// Paper (light, default) and ink (dark) themes. The choice is remembered per browser.
// The theme is pure CSS on <html data-theme="...">, so no component DOM changes between themes.
type Theme = 'light' | 'dark';
// A new key, so every visitor first sees the paper (day) theme once; after that their own choice sticks.
const KEY = 'gpulens-theme-v3';

const SUN =
  '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" aria-hidden="true"><circle cx="12" cy="12" r="4"/><path d="M12 2.5v2.2M12 19.3v2.2M4.6 4.6l1.6 1.6M17.8 17.8l1.6 1.6M2.5 12h2.2M19.3 12h2.2M4.6 19.4l1.6-1.6M17.8 6.2l1.6-1.6"/></svg>';
const MOON =
  '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M20.5 14.2A8.5 8.5 0 1 1 9.8 3.5a6.8 6.8 0 0 0 10.7 10.7z"/></svg>';

function read(): Theme {
  try {
    const v = window.localStorage.getItem(KEY);
    if (v === 'dark' || v === 'light') return v;
  } catch {
    /* storage can be unavailable; fall through */
  }
  return 'light';
}

export function applyTheme(t: Theme) {
  document.documentElement.setAttribute('data-theme', t);
  const group = document.getElementById('theme-toggle');
  if (group) {
    group.querySelectorAll('button').forEach((b) => {
      b.setAttribute('aria-pressed', b.getAttribute('data-theme-value') === t ? 'true' : 'false');
    });
  }
}

export function currentTheme(): Theme {
  return document.documentElement.getAttribute('data-theme') === 'light' ? 'light' : 'dark';
}

export function initTheme() {
  applyTheme(read());
}

function choose(t: Theme) {
  try {
    window.localStorage.setItem(KEY, t);
  } catch {
    /* ignore */
  }
  applyTheme(t);
}

/** A small sun | moon switch, plain DOM outside the app root. */
export function mountThemeToggle() {
  if (document.getElementById('theme-toggle')) return;
  const group = document.createElement('div');
  group.id = 'theme-toggle';
  group.className = 'theme-toggle';
  group.setAttribute('role', 'group');
  group.setAttribute('aria-label', 'Colour theme');
  for (const [value, label, icon] of [
    ['light', 'Paper (light) theme', SUN],
    ['dark', 'Ink (dark) theme', MOON],
  ] as [Theme, string, string][]) {
    const b = document.createElement('button');
    b.type = 'button';
    b.title = label;
    b.setAttribute('aria-label', label);
    b.setAttribute('data-theme-value', value);
    b.innerHTML = icon;
    b.addEventListener('click', () => choose(value));
    group.appendChild(b);
  }
  document.body.appendChild(group);
  applyTheme(currentTheme());
}
