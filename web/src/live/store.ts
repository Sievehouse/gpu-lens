// Polling store for the live console. Lives outside the component tree so that polling survives re-renders;
// components read `live` and call `requestRender` through the helpers below.
import { requestRender } from '../jsx-runtime';
import { ActivityEntry, FleetView, HistoryPoint, LiveState } from './types';
import * as api from './api';

export interface LiveStore {
  active: boolean;
  state: LiveState | null;
  history: HistoryPoint[];
  historyEpoch: number;
  activity: ActivityEntry[];
  error: string;
  notice: string;
  busy: string; // name of the action currently running ('' when none)
  lastFetchAt: number;
  fetchCount: number;
  paused: boolean; // the user stopped collection from the console
  fleet: FleetView | null;
  fleetAt: number;
  fleetFocus: boolean; // the Fleet tab is open: refresh it more often
}

export const live: LiveStore = {
  active: false,
  state: null,
  history: [],
  historyEpoch: -1,
  activity: [],
  error: '',
  notice: '',
  busy: '',
  lastFetchAt: 0,
  fetchCount: 0,
  paused: false,
  fleet: null,
  fleetAt: 0,
  fleetFocus: false,
};

const MAX_ACTIVITY = 500;
let timer: number | undefined;
let inFlight = false;
let consecutiveErrors = 0;

function absorb(next: LiveState, replace: boolean) {
  const h = next.history;
  if (replace || h.epoch !== live.historyEpoch) {
    live.history = h.points.slice();
    live.historyEpoch = h.epoch;
  } else if (h.points.length) {
    const lastKnown = live.history.length ? live.history[live.history.length - 1].seq : 0;
    for (const p of h.points) if (p.seq > lastKnown) live.history.push(p);
  }
  const cap = Math.max(60, h.capacity || 900);
  if (live.history.length > cap) live.history.splice(0, live.history.length - cap);

  if (replace) live.activity = next.activity.slice();
  else {
    const lastKnown = live.activity.length ? live.activity[live.activity.length - 1].seq : 0;
    for (const a of next.activity) if (a.seq > lastKnown) live.activity.push(a);
  }
  if (live.activity.length > MAX_ACTIVITY) live.activity.splice(0, live.activity.length - MAX_ACTIVITY);
  live.state = next;
}

function lastSeq(): number {
  return live.history.length ? live.history[live.history.length - 1].seq : 0;
}
function lastActivitySeq(): number {
  return live.activity.length ? live.activity[live.activity.length - 1].seq : 0;
}

export async function refresh(): Promise<void> {
  if (inFlight) return;
  inFlight = true;
  try {
    let next = await api.fetchLiveState(lastSeq(), lastActivitySeq());
    if (next.history.epoch !== live.historyEpoch && live.historyEpoch !== -1) {
      // The server rebuilt the series (new session or config change): reload everything from the start.
      next = await api.fetchLiveState(0, 0);
      absorb(next, true);
    } else {
      absorb(next, live.historyEpoch === -1);
    }
    live.error = '';
    live.lastFetchAt = Date.now();
    live.fetchCount++;
    consecutiveErrors = 0;
    if (Date.now() - live.fleetAt > (live.fleetFocus ? 3000 : 15000)) {
      live.fleetAt = Date.now();
      try {
        live.fleet = await api.fetchFleet();
      } catch {
        /* the fleet view is optional */
      }
    }
  } catch (err: any) {
    consecutiveErrors++;
    live.error = err?.message || 'Could not reach the GPU Lens backend.';
  } finally {
    inFlight = false;
    requestRender();
  }
}

function nextDelayMs(): number {
  const s = live.state?.status.interval_seconds || 2;
  let ms = Math.min(10, Math.max(1, s)) * 1000;
  if (typeof document !== 'undefined' && document.hidden) ms = Math.max(ms, 5000);
  if (consecutiveErrors > 0) ms = Math.min(15000, ms * (1 + consecutiveErrors));
  return ms;
}

function loop() {
  if (!live.active) return;
  timer = window.setTimeout(async () => {
    await refresh();
    loop();
  }, nextDelayMs());
}

async function act(name: string, fn: () => Promise<LiveState>, reset = false): Promise<boolean> {
  live.busy = name;
  live.notice = '';
  requestRender();
  try {
    const next = await fn();
    absorb(next, reset || next.history.epoch !== live.historyEpoch);
    live.error = '';
    return true;
  } catch (err: any) {
    live.notice = '';
    live.error = err?.message || 'Request failed.';
    return false;
  } finally {
    live.busy = '';
    requestRender();
  }
}

/** Enter the console: start collection on the server (unless the user paused it) and begin polling. */
export async function enter(): Promise<void> {
  if (live.active) return;
  live.active = true;
  live.paused = false;
  live.error = '';
  requestRender();
  await act('start', () => api.startLive(), true);
  loop();
}

export function leave(): void {
  live.active = false;
  if (timer !== undefined) window.clearTimeout(timer);
  timer = undefined;
  requestRender();
}

export async function start(): Promise<void> {
  live.paused = false;
  await act('start', () => api.startLive());
}
export async function stop(): Promise<void> {
  live.paused = true;
  await act('stop', () => api.stopLive());
}
export async function recheck(): Promise<void> {
  if (await act('recheck', () => api.recheckLive())) live.notice = 'Re-checked every source just now.';
  requestRender();
}
export async function saveConfig(patch: Record<string, unknown>): Promise<boolean> {
  const ok = await act('config', () => api.saveLiveConfig(patch), true);
  if (ok) live.notice = 'Connection settings saved — the session restarted and every source was checked again.';
  requestRender();
  return ok;
}
export async function saveThresholds(overrides: Record<string, number> | null, reset: boolean): Promise<boolean> {
  const ok = await act('thresholds', () => api.saveThresholds(overrides, reset));
  if (ok) live.notice = reset ? 'Thresholds are back to their defaults.' : 'Thresholds updated — rules re-evaluated on the next cycle.';
  requestRender();
  return ok;
}
export function setInterval_(seconds: number): Promise<boolean> {
  return saveConfig({ interval_seconds: seconds });
}

export function clearMessages() {
  live.error = '';
  live.notice = '';
  requestRender();
}

// Export everything the console currently knows as one JSON document.
export function exportSnapshot(): string {
  const s = live.state;
  return JSON.stringify(
    {
      exported_at: new Date().toISOString(),
      note: 'GPU Lens live snapshot. Read-only telemetry; secrets are never included.',
      state: s ? { ...s, history: { ...s.history, points: live.history }, activity: live.activity } : null,
    },
    null,
    2
  );
}

export async function testAlerts(): Promise<void> {
  live.busy = 'alerts';
  live.notice = '';
  requestRender();
  try {
    const res = await api.testAlerts();
    const ok = res.results.filter((r) => r.ok).length;
    const failed = res.results.filter((r) => !r.ok).map((r) => `${r.kind}: ${r.error}`);
    if (live.state) live.state = { ...live.state, alerts: res.status };
    if (failed.length) live.error = `Test alert: ${ok} of ${res.results.length} destination(s) accepted it. ${failed.join('; ')}`;
    else live.notice = `Test alert delivered to ${ok} destination(s).`;
  } catch (err: any) {
    live.error = err?.message || 'Test alert failed.';
  } finally {
    live.busy = '';
    requestRender();
  }
}
