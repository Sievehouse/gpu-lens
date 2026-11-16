import { h, Fragment, mountApp } from './jsx-runtime';
import { App } from './App';
import { LiveConsole } from './live/LiveConsole';
import { initMode, isLive, mountLauncher, setLive } from './mode';
import { initTheme, mountThemeToggle } from './theme';

const rootEl = document.getElementById('root');
if (rootEl) {
  initTheme();
  initMode();
  mountLauncher();
  mountThemeToggle();
  mountApp(() => (isLive() ? <LiveConsole onExit={() => setLive(false)} /> : <App />), rootEl);
}
