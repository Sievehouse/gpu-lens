import { LiveState } from './types';

async function request(method: string, path: string, body?: unknown): Promise<LiveState> {
  const init: RequestInit = { method, headers: { Accept: 'application/json' }, cache: 'no-store' };
  if (method === 'POST') {
    (init.headers as Record<string, string>)['Content-Type'] = 'application/json';
    init.body = JSON.stringify(body || {});
  }
  const resp = await fetch(path, init);
  const text = await resp.text();
  if (!resp.ok) {
    let msg = text;
    try {
      msg = JSON.parse(text).error || text;
    } catch {
      /* keep raw text */
    }
    throw new Error(msg || `HTTP ${resp.status}`);
  }
  return JSON.parse(text) as LiveState;
}

export function fetchLiveState(since: number, activitySince: number): Promise<LiveState> {
  return request('GET', `/api/v1/live/state?since=${since}&activity_since=${activitySince}`);
}
export const startLive = (intervalSeconds?: number) => request('POST', '/api/v1/live/start', intervalSeconds ? { interval_seconds: intervalSeconds } : {});
export const stopLive = () => request('POST', '/api/v1/live/stop');
export const recheckLive = () => request('POST', '/api/v1/live/recheck');
export const saveLiveConfig = (patch: Record<string, unknown>) => request('POST', '/api/v1/live/config', patch);
export const saveThresholds = (overrides: Record<string, number> | null, reset: boolean) =>
  request('POST', '/api/v1/live/thresholds', reset ? { reset: true } : { overrides: overrides || {} });
