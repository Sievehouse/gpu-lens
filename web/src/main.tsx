import { h, mountApp } from './jsx-runtime';
import { App } from './App';

const rootEl = document.getElementById('root');
if (rootEl) {
  mountApp(() => <App />, rootEl);
}
