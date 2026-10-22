import {
  OverviewResponse,
  Incident,
  RuleDefinition,
  ScenarioSummary,
} from './types';

async function getJSON<T>(url: string): Promise<T> {
  const resp = await fetch(url, {
    headers: { Accept: 'application/json' },
  });
  if (!resp.ok) {
    const text = await resp.text();
    throw new Error(`HTTP ${resp.status}: ${text}`);
  }
  return resp.json() as Promise<T>;
}

export async function fetchScenarios(): Promise<{ mode: string; scenarios: ScenarioSummary[] }> {
  return getJSON('/api/v1/scenarios');
}

export async function selectScenario(scenarioId: string): Promise<OverviewResponse> {
  const resp = await fetch('/api/v1/scenarios/select', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ scenario_id: scenarioId }),
  });
  if (!resp.ok) {
    const text = await resp.text();
    throw new Error(`HTTP ${resp.status}: ${text}`);
  }
  return resp.json() as Promise<OverviewResponse>;
}

export async function fetchOverview(scenarioId?: string): Promise<OverviewResponse> {
  const q = scenarioId ? `?scenario=${encodeURIComponent(scenarioId)}` : '';
  return getJSON(`/api/v1/overview${q}`);
}

export async function fetchIncidents(params: {
  severity?: string;
  category?: string;
  q?: string;
}): Promise<{ scenario_id: string; data_mode: string; count: number; incidents: Incident[] }> {
  const sp = new URLSearchParams();
  if (params.severity && params.severity !== 'all') sp.set('severity', params.severity);
  if (params.category && params.category !== 'all') sp.set('category', params.category);
  if (params.q) sp.set('q', params.q);
  const qs = sp.toString();
  return getJSON(`/api/v1/incidents${qs ? '?' + qs : ''}`);
}

export async function fetchIncidentDetail(id: string): Promise<Incident> {
  return getJSON(`/api/v1/incidents/${encodeURIComponent(id)}`);
}

export async function fetchCapacity(): Promise<any> {
  return getJSON('/api/v1/capacity');
}

export async function fetchSources(): Promise<any> {
  return getJSON('/api/v1/sources');
}

export async function fetchRules(): Promise<{ count: number; rules: RuleDefinition[] }> {
  return getJSON('/api/v1/rules');
}
