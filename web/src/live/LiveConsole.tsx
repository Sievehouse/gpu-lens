// "Go Live" console — part 2 of the UI. Part 1 (the simulated demo in App.tsx) is not touched by anything here.
import { h, Fragment, useState, useEffect } from '../jsx-runtime';
import * as store from './store';
import { live } from './store';
import { Incident } from '../types';
import { renderCapacityTab, renderIncidentDetailTab, renderIncidentsTab, renderRulesTab } from '../App';
import { Badge, download, Empty, num } from './ui';
import { GPUFilters, renderCommandCenter, renderGPUs, renderHost, renderPreflight, verdictClass } from './views_a';
import {
  ConnDraft, buildConfigPatch, draftFromConfig, renderActivity, renderConnections, renderSources, renderThresholds, renderWorkloads,
} from './views_b';
import { renderAlerting, renderFleet, renderPersistence, viewerBanner } from './views_c';

const TABS: [string, string][] = [
  ['command', 'Command Center'],
  ['fleet', 'Fleet'],
  ['preflight', 'Check Everything'],
  ['gpus', 'GPUs'],
  ['incidents', 'Incidents'],
  ['capacity', 'Capacity'],
  ['workloads', 'Workloads'],
  ['sources', 'Sources'],
  ['host', 'Host Vitals'],
  ['connections', 'Connections'],
  ['thresholds', 'Thresholds'],
  ['activity', 'Activity'],
  ['rules', 'Rules'],
];

function initialTab(): string {
  try {
    const t = new URLSearchParams(window.location.search).get('live_tab') || '';
    return TABS.some((x) => x[0] === t) ? t : 'command';
  } catch {
    return 'command';
  }
}

export function LiveConsole(props: { onExit: () => void }) {
  const [tab, setTabRaw] = useState<string>(initialTab());
  const [gpuFilters, setGpuFilters] = useState<GPUFilters>({ q: '', node: 'all', sort: 'index', status: 'all' });
  const [openGpu, setOpenGpu] = useState<string>('');
  const [sev, setSev] = useState('all');
  const [cat, setCat] = useState('all');
  const [query, setQuery] = useState('');
  const [selected, setSelected] = useState<Incident | null>(null);
  const [incView, setIncView] = useState<string>('queue');
  const [conn, setConn] = useState<ConnDraft | null>(null);
  const [thDraft, setThDraft] = useState<Record<string, string>>({});
  const [actFilter, setActFilter] = useState('all');

  function setTab(t: string) {
    setTabRaw(t);
    try {
      const u = new URL(window.location.href);
      u.searchParams.set('live_tab', t);
      window.history.replaceState(null, '', u.toString());
    } catch {
      /* non-critical */
    }
  }

  useEffect(() => {
    store.enter();
    return () => store.leave();
  }, []);

  const s = live.state;
  live.fleetFocus = tab === 'fleet';

  // Seed the connection form once the first state arrives (and again whenever the server config changes underneath us).
  const cfgKey = s ? JSON.stringify(s.config) : '';
  useEffect(() => {
    if (s) setConn(draftFromConfig(s.config));
  }, [cfgKey]);

  if (!s) {
    return (
      <div className="main-container">
        <div className="panel">
          <div className="panel-title">{live.error ? 'Cannot reach the GPU Lens backend' : 'Going live…'}</div>
          <p style={{ color: 'var(--text-secondary)', marginTop: '8px' }}>
            {live.error || 'Starting collection and checking every telemetry source (host, NVIDIA driver, DCGM, Prometheus, Kubernetes).'}
          </p>
          <div style={{ marginTop: '12px', display: 'flex', gap: '8px' }}>
            <button className="btn" onClick={() => store.refresh()}>Retry</button>
            <button className="btn" onClick={props.onExit}>Open the simulation →</button>
          </div>
        </div>
      </div>
    );
  }

  if (!s.status.supported) {
    return (
      <div className="main-container">
        <div className="panel">
          <div className="panel-title">Go Live is switched off</div>
          <p style={{ color: 'var(--text-secondary)', marginTop: '8px' }}>{s.status.unsupported_reason || 'The live console is disabled in this build.'}</p>
          <button className="btn" style={{ marginTop: '12px' }} onClick={props.onExit}>Open the simulation →</button>
        </div>
      </div>
    );
  }

  const ov = s.overview;
  const canOperate = s.status.can_operate !== false;
  const fleetOn = !!(live.fleet && live.fleet.enabled);
  const tabs = TABS.filter(([k]) => k !== 'fleet' || fleetOn || tab === 'fleet');
  const incidents: Incident[] = (ov && ov.active_incidents) || [];
  const running = s.status.state === 'running';
  const stale = running && live.lastFetchAt > 0 && Date.now() - live.lastFetchAt > Math.max(8000, s.status.interval_seconds * 4000);
  const filteredIncidents = incidents.filter((inc) => {
    if (sev !== 'all' && inc.severity !== sev) return false;
    if (cat !== 'all' && inc.category !== cat) return false;
    if (query.trim()) {
      const q = query.toLowerCase();
      const hay = `${inc.title} ${inc.summary} ${inc.rule_id} ${(inc.affected_resources || []).join(' ')}`.toLowerCase();
      if (!hay.includes(q)) return false;
    }
    return true;
  });
  const openIncident = (inc: Incident) => {
    setSelected(inc);
    setIncView('detail');
    setTab('incidents');
  };
  const selectedNow = selected ? incidents.find((i) => i.fingerprint === selected.fingerprint) || selected : incidents[0] || null;

  async function saveConn() {
    if (!conn || !s) return;
    const patch = buildConfigPatch(conn, s.config);
    if (await store.saveConfig(patch)) setConn(null);
  }
  async function applyThresholds() {
    const o: Record<string, number> = {};
    for (const k of Object.keys(thDraft)) {
      const v = thDraft[k];
      if (v !== '' && !Number.isNaN(Number(v)) && Number(v) !== s!.thresholds[k]) o[k] = Number(v);
    }
    if (Object.keys(o).length && (await store.saveThresholds(o, false))) setThDraft({});
  }
  async function resetThresholds() {
    if (await store.saveThresholds(null, true)) setThDraft({});
  }

  let body: any = null;
  switch (tab) {
    case 'command': body = renderCommandCenter(live, s, setTab, props.onExit); break;
    case 'fleet': body = renderFleet(live); break;
    case 'preflight': body = renderPreflight(live, s, () => store.recheck()); break;
    case 'gpus': body = renderGPUs(live, s, gpuFilters, setGpuFilters, openGpu, setOpenGpu); break;
    case 'incidents':
      body = (
        <div>
          <div className="lv-subnav">
            {[['queue', `Queue (${incidents.length})`], ['detail', 'Detail']].map(([k, l]) => (
              <button className={`lv-subtab ${incView === k ? 'active' : ''}`} onClick={() => setIncView(k)}>{l}</button>
            ))}
          </div>
          {incView === 'queue'
            ? renderIncidentsTab(filteredIncidents, sev, setSev, cat, setCat, query, setQuery, openIncident)
            : renderIncidentDetailTab(selectedNow, incidents, setSelected)}
          {incidents.length === 0 && s.gpus.length === 0 && (
            <div className="blindspot-box"><strong>No telemetry, no verdict.</strong> The incident list is empty because no GPU is reporting — not because the fleet is healthy.</div>
          )}
        </div>
      );
      break;
    case 'capacity':
      body = ov && ov.capacity_summary && s.gpus.length ? renderCapacityTab(ov.capacity_summary) : (
        <div className="panel"><Empty title="Capacity needs GPU telemetry">Capacity math runs as soon as a source reports GPUs.</Empty></div>
      );
      break;
    case 'workloads': body = renderWorkloads(s); break;
    case 'sources': body = renderSources(s, () => store.recheck(), live.busy); break;
    case 'host': body = renderHost(live, s); break;
    case 'connections':
      body = conn
        ? (
          <div>
            {renderConnections(s, conn, setConn, saveConn, () => setConn(draftFromConfig(s.config)), live.busy)}
            {renderAlerting(s, canOperate, live.busy, () => store.testAlerts())}
            {renderPersistence(s)}
          </div>
        )
        : <div className="panel">Loading…</div>;
      break;
    case 'thresholds': body = renderThresholds(s, thDraft, setThDraft, applyThresholds, resetThresholds, live.busy, canOperate); break;
    case 'activity': body = renderActivity(live, s, actFilter, setActFilter); break;
    case 'rules': body = renderRulesTab(s.rules || []); break;
    default: body = null;
  }

  const pf = s.preflight;
  const counts = (ov && ov.active_incident_counts) || { critical: 0, warning: 0, inconclusive: 0 };
  const tabCount = (k: string): any => {
    if (k === 'incidents') return incidents.length;
    if (k === 'gpus') return s.gpus.length;
    if (k === 'activity') return live.activity.length;
    if (k === 'fleet') return live.fleet ? live.fleet.totals.members : null;
    return null;
  };

  return (
    <div className="lv-root">
      <div className="lv-topbar">
        <div className="lv-live-pill"><span className={`lv-pulse ${running && !stale ? 'on' : ''}`} />{running ? (stale ? 'STALLED' : 'LIVE') : 'PAUSED'}</div>
        <span className="lv-note mono">
          {s.status.runtime} v{s.status.version} · {s.status.hostname} · cycle {s.status.tick} · every {s.status.interval_seconds}s
          {s.status.last_tick_duration_ms ? ` · last cycle ${num(s.status.last_tick_duration_ms, 0)} ms` : ''}
        </span>
        <div style={{ flex: '1' }} />
        {s.status.auth_enabled && (
          <span className="lv-note" title="Signed in with HTTP basic authentication">signed in as <b className="mono">{s.status.user}</b> ({s.status.role})</span>
        )}
        {running
          ? <button className="btn" disabled={live.busy !== '' || !canOperate} title={canOperate ? '' : 'Viewer role: read-only'} onClick={() => store.stop()}>Pause collection</button>
          : <button className="btn btn-primary" disabled={live.busy !== ''} onClick={() => store.start()}>Resume collection</button>}
        <button className="btn" disabled={live.busy !== '' || !canOperate} title={canOperate ? '' : 'Viewer role: read-only'} onClick={() => store.recheck()}>{live.busy === 'recheck' ? 'Checking…' : 'Re-check everything'}</button>
        <button className="btn" onClick={() => download(`gpu-lens-live-${new Date().toISOString().replace(/[:.]/g, '-')}.json`, store.exportSnapshot())}>Export JSON</button>
        <button className="btn" onClick={props.onExit}>Open the simulation →</button>
      </div>

      {viewerBanner(s)}
      {s.status.emulated && (
        <div className="lv-emulated">
          <b>EMULATED DATA</b> — the connected DCGM/Kubernetes endpoints are the GPU Lens <span className="mono">Live Lab</span> synthetic emulator, not real
          hardware. Everything below is computed live, but the GPUs do not exist.
        </div>
      )}
      {live.error && (
        <div className="lv-alert error">
          <span><b>Connection problem:</b> {live.error}</span>
          <button className="btn" onClick={() => store.clearMessages()}>Dismiss</button>
        </div>
      )}
      {live.notice && (
        <div className="lv-alert ok">
          <span>{live.notice}</span>
          <button className="btn" onClick={() => store.clearMessages()}>Dismiss</button>
        </div>
      )}
      {!running && (
        <div className="lv-alert warn">
          <span><b>Collection is not running.</b> {s.status.state_reason || 'Press Resume to start collecting again.'}</span>
          <button className="btn btn-primary" onClick={() => store.start()}>Resume</button>
        </div>
      )}

      <header className="top-header">
        <div className="brand-group">
          <div className="brand-logo live">GL</div>
          <div>
            <div className="brand-title">GPU Lens <span className="lv-live-tag">LIVE</span></div>
            <div className="brand-subtitle">
              Cluster: <span className="mono" style={{ color: '#e2e8f0' }}>{(ov && ov.cluster_name) || s.status.hostname}</span> · read-only
            </div>
          </div>
        </div>
        <nav className="nav-tabs lv-tabs">
          {tabs.map(([k, label]) => (
            <button className={`nav-tab ${tab === k ? 'active' : ''}`} onClick={() => setTab(k)}>
              {label}
              {tabCount(k) !== null && <span className="tab-count">{tabCount(k)}</span>}
            </button>
          ))}
        </nav>
      </header>

      <main className="main-container">
        <div className={`health-banner ${verdictClass(pf.verdict)}`}>
          <div>
            <div style={{ display: 'flex', alignItems: 'center', gap: '10px', flexWrap: 'wrap' }}>
              <span className={`badge ${verdictClass(pf.verdict)}`}>{pf.verdict}</span>
              <strong style={{ fontSize: '14.5px' }}>{pf.verdict_text}</strong>
            </div>
            {ov && s.gpus.length > 0 && (
              <div className="lv-note" style={{ marginTop: '4px' }}>{ov.fleet_health_headline}</div>
            )}
          </div>
          <div className="mono" style={{ fontSize: '12px' }}>
            Critical: {counts.critical || 0} · Warning: {counts.warning || 0} · Inconclusive: {counts.inconclusive || 0}
          </div>
        </div>
        {body}
        <div className="lv-footer lv-note">
          Read-only by construction: every request GPU Lens makes to your infrastructure is a GET/HEAD or an allow-listed nvidia-smi / amd-smi query.
          {s.alerts && s.alerts.enabled ? ' Alerts are POSTed only to the destinations configured at startup.' : ''} {ov && <Badge kind="info">{ov.data_mode}</Badge>}
        </div>
      </main>
    </div>
  );
}
