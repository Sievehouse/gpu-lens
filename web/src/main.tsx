import { h, Fragment, mountApp } from './jsx-runtime';
import { App } from './App';
import { LiveConsole } from './live/LiveConsole';
import { initMode, isLive, mountLauncher, setLive } from './mode';

const rootEl = document.getElementById('root');
if (rootEl) {
  initMode();
  mountLauncher();
  mountApp(() => (isLive() ? <LiveConsole onExit={() => setLive(false)} /> : <App />), rootEl);
}
