// Light / dark theme. Light (white background) is the default; the choice is remembered per browser.
// The theme is pure CSS on <html data-theme="...">, so no component DOM changes between themes.
type Theme = 'light' | 'dark';
const KEY = 'gpulens-theme';

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
  const btn = document.getElementById('theme-toggle');
  if (btn) {
    btn.textContent = t === 'light' ? '☾ Dark mode' : '☀ Light mode';
    btn.setAttribute('aria-label', t === 'light' ? 'Switch to dark mode' : 'Switch to light mode');
  }
}

export function currentTheme(): Theme {
  return document.documentElement.getAttribute('data-theme') === 'dark' ? 'dark' : 'light';
}

export function initTheme() {
  applyTheme(read());
}

/** A small fixed button, plain DOM outside the app root. */
export function mountThemeToggle() {
  if (document.getElementById('theme-toggle')) return;
  const btn = document.createElement('button');
  btn.id = 'theme-toggle';
  btn.type = 'button';
  btn.className = 'theme-toggle';
  btn.addEventListener('click', () => {
    const next: Theme = currentTheme() === 'light' ? 'dark' : 'light';
    try {
      window.localStorage.setItem(KEY, next);
    } catch {
      /* ignore */
    }
    applyTheme(next);
  });
  document.body.appendChild(btn);
  applyTheme(currentTheme());
}
