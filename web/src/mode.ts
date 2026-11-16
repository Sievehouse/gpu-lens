// Switches between part 1 (simulated demo) and part 2 (Go Live). Part 1's DOM is never modified: the launcher
// button is a separate element outside the app root.
import { requestRender } from './jsx-runtime';

let liveMode = false;

export function isLive(): boolean {
  return liveMode;
}

function syncUrl() {
  try {
    const u = new URL(window.location.href);
    if (liveMode) u.searchParams.set('view', 'live');
    else {
      u.searchParams.delete('view');
      u.searchParams.delete('live_tab');
    }
    window.history.replaceState(null, '', u.toString());
  } catch {
    /* ignore */
  }
  const btn = document.getElementById('golive-launcher');
  if (btn) btn.style.display = liveMode ? 'none' : '';
}

export function setLive(on: boolean) {
  if (liveMode === on) return;
  liveMode = on;
  syncUrl();
  window.scrollTo(0, 0);
  requestRender();
}

export function initMode() {
  try {
    liveMode = new URLSearchParams(window.location.search).get('view') === 'live';
  } catch {
    liveMode = false;
  }
}

/** The "Go Live" launcher: plain DOM appended to <body>, outside the demo's #root. */
export function mountLauncher() {
  if (document.getElementById('golive-launcher')) return;
  const btn = document.createElement('button');
  btn.id = 'golive-launcher';
  btn.type = 'button';
  btn.className = 'golive-launcher';
  btn.title = 'Switch from the simulated demo to live telemetry from this machine and your GPU infrastructure';
  btn.innerHTML = '<span class="golive-dot"></span><span class="golive-text">Go Live</span>';
  btn.addEventListener('click', () => setLive(true));
  document.body.appendChild(btn);
  btn.style.display = liveMode ? 'none' : '';
}
