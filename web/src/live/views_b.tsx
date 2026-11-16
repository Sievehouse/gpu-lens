// Workloads, sources, connections, thresholds, activity and incidents views of the live console.
import { h, Fragment } from '../jsx-runtime';
import { LiveStore } from './store';
import { LiveState } from './types';
import { Badge, Empty, Sparkline, STATE_LABEL, ago, clock, duration, mib, num } from './ui';
import { stateKind } from './views_a';

export function renderWorkloads(s: LiveState) {
  const { nodes, pods, events, processes } = s;
  const k8s = s.sources.find((x) => x.id === 'kubernetes');
  const k8sUp = !!k8s && (k8s.state === 'connected' || k8s.state === 'degraded');
  return (
    <div>
      {!k8sUp && (
        <div className="blindspot-box">
          <strong>No Kubernetes connection.</strong> Nodes and workloads below are inferred from GPU telemetry only (host processes), so scheduling,
          pending-pod and pod-attribution findings are unavailable. {k8s?.hint}
        </div>
      )}
      <div className="panel">
        <div className="panel-header"><div className="panel-title">Nodes</div><span className="lv-note">{nodes.length}</span></div>
        {nodes.length === 0 ? <Empty title="No nodes">No GPU nodes have been discovered yet.</Empty> : (
          <table className="data-table">
            <thead><tr><th>Node</th><th>Model</th><th>Ready</th><th>Schedulable</th><th>GPUs (alloc / allocatable / capacity)</th><th>Conditions</th></tr></thead>
            <tbody>
              {nodes.map((n: any) => (
                <tr key={n.name}>
                  <td className="mono">{n.name}</td><td>{n.gpu_model || '—'}</td>
                  <td><Badge kind={n.ready ? 'healthy' : 'critical'}>{n.ready ? 'Ready' : 'NotReady'}</Badge></td>
                  <td>{n.schedulable ? 'yes' : <Badge kind="warning">cordoned</Badge>}</td>
                  <td className="mono">{n.gpu_allocated} / {n.gpu_allocatable} / {n.gpu_capacity}</td>
                  <td>{(n.conditions || []).filter((c: any) => c.status === 'True' && c.type !== 'Ready').map((c: any) => <Badge kind="warning">{c.type}</Badge>)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
      <div className="panel">
        <div className="panel-header"><div className="panel-title">GPU workloads</div><span className="lv-note">{pods.length}</span></div>
        {pods.length === 0 ? <Empty title="No GPU workloads">No pod or host process is holding a GPU right now.</Empty> : (
          <table className="data-table">
            <thead><tr><th>Workload</th><th>Owner</th><th>Phase</th><th>Node</th><th>GPUs</th><th>Running for</th><th>Restarts</th><th>Detail</th></tr></thead>
            <tbody>
              {pods.map((p: any) => (
                <tr key={p.uid || p.namespace + p.name}>
                  <td className="mono">{p.namespace}/{p.name}</td><td>{p.owner_kind ? `${p.owner_kind}/${p.owner_name}` : '—'}</td>
                  <td><Badge kind={p.phase === 'Running' ? 'healthy' : p.phase === 'Pending' ? 'warning' : p.phase === 'Failed' ? 'critical' : 'info'}>{p.phase}</Badge></td>
                  <td className="mono">{p.node_name || '—'}</td><td>{p.requested_gpus}</td>
                  <td>{p.started_at ? duration((Date.now() - new Date(p.started_at).getTime()) / 1000) : '—'}</td>
                  <td>{p.restart_count}</td>
                  <td className="lv-note">{p.scheduler_reason || p.status_detail || ''}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
      <div className="panel">
        <div className="panel-header"><div className="panel-title">GPU compute processes (nvidia-smi)</div><span className="lv-note">{processes.length}</span></div>
        {processes.length === 0 ? <Empty title="No process data">Needs the local NVIDIA driver (nvidia-smi) on this machine; DCGM and Prometheus do not report processes.</Empty> : (
          <table className="data-table">
            <thead><tr><th>PID</th><th>Name</th><th>Node</th><th>GPU</th><th>VRAM</th></tr></thead>
            <tbody>{processes.map((p) => <tr><td className="mono">{p.pid}</td><td className="mono">{p.name}</td><td>{p.node_name}</td><td>{p.gpu_index}</td><td>{mib(p.used_memory_mib)}</td></tr>)}</tbody>
          </table>
        )}
      </div>
      <div className="panel">
        <div className="panel-header"><div className="panel-title">Kubernetes events</div><span className="lv-note">{events.length}</span></div>
        {events.length === 0 ? <Empty title="No events">No GPU-relevant Kubernetes events were returned.</Empty> : (
          <table className="data-table">
            <thead><tr><th>Type</th><th>Reason</th><th>Object</th><th>Message</th><th>Count</th><th>Last seen</th></tr></thead>
            <tbody>
              {events.slice(0, 100).map((e: any) => (
                <tr><td><Badge kind={e.type === 'Warning' ? 'warning' : 'info'}>{e.type}</Badge></td><td>{e.reason}</td>
                  <td className="mono">{e.involved_object_kind}/{e.involved_object_name}</td><td>{e.message}</td><td>{e.count}</td><td className="mono">{clock(e.last_timestamp)}</td></tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}

export function renderSources(s: LiveState, recheck: () => void, busy: string) {
  const now = Date.now();
  const metrics = s.sources_detail?.exported_metrics || {};
  const metricNames = Object.keys(metrics).sort();
  return (
    <div>
      <div className="lv-toolbar">
        <button className="btn btn-primary" disabled={busy !== ''} onClick={recheck}>Probe every source now</button>
        <span className="lv-note">Every probe is read-only: GET/HEAD requests, or a fixed allow-list of nvidia-smi query flags.</span>
      </div>
      <div className="lv-source-grid">
        {s.sources.map((src) => (
          <div className={`lv-source ${src.state}`} key={src.id}>
            <div className="lv-gpu-head">
              <div>
                <div style={{ fontWeight: 700 }}>{src.name}</div>
                <div className="lv-note mono" style={{ wordBreak: 'break-all' }}>{src.endpoint}</div>
              </div>
              <div className="lv-badges">
                <Badge kind={stateKind(src.state)}>{STATE_LABEL[src.state] || src.state}</Badge>
                {src.emulated && <Badge kind="info">emulated</Badge>}
                {src.explicit && <Badge kind="healthy">configured</Badge>}
              </div>
            </div>
            <div style={{ margin: '8px 0' }}>{src.detail}</div>
            {src.hint && src.state !== 'connected' && <div className="lv-hint">→ {src.hint}</div>}
            {src.last_error && src.state !== 'connected' && <div className="lv-error mono">{src.last_error}</div>}
            <div className="lv-source-stats">
              <div><span>Last success</span><b>{ago(src.last_success_at, now)}</b></div>
              <div><span>Latency</span><b>{src.last_latency_ms === null ? '—' : `${src.last_latency_ms.toFixed(0)} ms`}</b></div>
              <div><span>OK / failed</span><b>{src.success_count} / {src.failure_count}</b></div>
              <div><span>Refresh</span><b>{src.refresh_seconds ? `${src.refresh_seconds}s` : '—'}</b></div>
            </div>
            {src.latency_history_ms.length > 1 && <Sparkline values={src.latency_history_ms} color="#38bdf8" width={300} height={30} />}
            {src.facts.length > 0 && (
              <table className="lv-kv" style={{ marginTop: '6px' }}><tbody>
                {src.facts.map((f) => <tr><td>{f.label}</td><td className="mono">{f.value}</td></tr>)}
              </tbody></table>
            )}
            <div className="lv-note" style={{ marginTop: '6px' }}>🔒 {src.read_only}</div>
          </div>
        ))}
      </div>
      <div className="panel">
        <div className="panel-header"><div className="panel-title">Metric coverage</div><span className="lv-note">which GPU signals the connected sources export</span></div>
        {metricNames.length === 0 ? <Empty title="No metric inventory yet">A DCGM exporter or Prometheus must be connected to list metric coverage.</Empty> : (
          <div className="lv-chips">
            {metricNames.map((m) => <span className={`lv-chip ${metrics[m] ? 'on' : 'off'}`}>{metrics[m] ? '✓' : '✕'} {m}</span>)}
          </div>
        )}
        <div className="lv-note" style={{ marginTop: '8px' }}>A metric that is not exported is reported as “not exported” — never as 0 and never as healthy.</div>
      </div>
      {s.notes && s.notes.length > 0 && <div className="panel"><div className="panel-title">Notes</div>{s.notes.map((n) => <div className="lv-note">• {n}</div>)}</div>}
    </div>
  );
}

// ---------- Connections editor ----------
export interface ConnDraft {
  cluster_name: string;
  interval: string;
  nvidia: boolean;
  host: boolean;
  dcgm: SrcDraft;
  prom: SrcDraft;
  k8s: SrcDraft;
}
export interface SrcDraft {
  enabled: boolean;
  endpoint: string;
  token: string;
  clear: boolean;
}

export function draftFromConfig(c: LiveState['config']): ConnDraft {
  const src = (x: LiveState['config']['dcgm_exporter']): SrcDraft => ({ enabled: x.enabled, endpoint: x.explicit ? x.endpoint : '', token: '', clear: false });
  return {
    cluster_name: c.cluster_name || '',
    interval: String(c.interval_seconds),
    nvidia: c.nvidia_smi_enabled,
    host: c.host_vitals_enabled,
    dcgm: src(c.dcgm_exporter), prom: src(c.prometheus), k8s: src(c.kubernetes),
  };
}

function patchFor(d: SrcDraft, cur: LiveState['config']['dcgm_exporter']) {
  const p: Record<string, unknown> = { enabled: d.enabled, endpoint: d.endpoint.trim() };
  if (d.token.trim()) p.bearer_token = d.token.trim();
  if (d.clear && cur.token_set) p.clear_token = true;
  return p;
}

export function buildConfigPatch(d: ConnDraft, c: LiveState['config']) {
  return {
    cluster_name: d.cluster_name.trim(),
    interval_seconds: Number(d.interval),
    nvidia_smi_enabled: d.nvidia,
    host_vitals_enabled: d.host,
    dcgm_exporter: patchFor(d.dcgm, c.dcgm_exporter),
    prometheus: patchFor(d.prom, c.prometheus),
    kubernetes: patchFor(d.k8s, c.kubernetes),
  };
}

function srcEditor(
  title: string, help: string, placeholder: string, d: SrcDraft, cur: LiveState['config']['dcgm_exporter'],
  set: (d: SrcDraft) => void, locked: boolean
) {
  return (
    <div className="lv-conn">
      <div className="lv-conn-head">
        <label className="lv-check-label">
          <input type="checkbox" checked={d.enabled} disabled={locked} onChange={(e: any) => set({ ...d, enabled: e.target.checked })} />
          <b>{title}</b>
        </label>
        <span className="lv-note">{cur.explicit ? 'endpoint set explicitly' : 'auto-probing the default address'}</span>
      </div>
      <div className="lv-note" style={{ marginBottom: '6px' }}>{help}</div>
      <input className="lv-input wide mono" placeholder={placeholder} value={d.endpoint} disabled={locked || !d.enabled}
        onInput={(e: any) => set({ ...d, endpoint: e.target.value })} />
      <input className="lv-input wide mono" type="password" autocomplete="off" disabled={locked || !d.enabled}
        placeholder={cur.token_set ? 'Bearer token is set — leave blank to keep it' : 'Bearer token (optional)'} value={d.token}
        onInput={(e: any) => set({ ...d, token: e.target.value })} />
      {cur.token_set && (
        <label className="lv-check-label lv-note">
          <input type="checkbox" checked={d.clear} disabled={locked} onChange={(e: any) => set({ ...d, clear: e.target.checked })} /> remove the stored token
        </label>
      )}
      {cur.token_file && <div className="lv-note">Token is read from a file configured at startup.</div>}
    </div>
  );
}

export function renderConnections(
  s: LiveState, d: ConnDraft, setD: (d: ConnDraft) => void, save: () => void, reload: () => void, busy: string
) {
  const locked = !s.status.config_mutable;
  return (
    <div>
      {locked && (
        <div className="blindspot-box">
          <strong>Connection settings are read-only here.</strong> {s.status.config_mutable_reason || 'Changing endpoints and tokens is disabled for this session.'}
        </div>
      )}
      <div className="panel">
        <div className="panel-header"><div className="panel-title">Telemetry sources</div>
          <span className="lv-note">Saving restarts the live session and re-checks every source from scratch.</span></div>
        <div className="lv-conn-grid">
          {srcEditor('DCGM exporter', 'Direct scrape of dcgm-exporter /metrics — the lowest-latency GPU source.', 'http://gpu-node:9400/metrics   (blank = probe localhost:9400)', d.dcgm, s.config.dcgm_exporter, (x) => setD({ ...d, dcgm: x }), locked)}
          {srcEditor('Prometheus', 'Read-only query API. Adds history backfill and cluster-wide coverage.', 'http://prometheus:9090   (blank = probe localhost:9090)', d.prom, s.config.prometheus, (x) => setD({ ...d, prom: x }), locked)}
          {srcEditor('Kubernetes API', 'Pods, nodes and events (GET/list/watch only). In a cluster the service account is used automatically; otherwise run kubectl proxy.', 'http://127.0.0.1:8001   (blank = probe kubectl proxy)', d.k8s, s.config.kubernetes, (x) => setD({ ...d, k8s: x }), locked)}
        </div>
      </div>
      <div className="panel">
        <div className="panel-header"><div className="panel-title">This machine &amp; session</div></div>
        <div className="lv-form-grid">
          <label className="lv-check-label"><input type="checkbox" checked={d.nvidia} disabled={locked} onChange={(e: any) => setD({ ...d, nvidia: e.target.checked })} /> Query the local NVIDIA driver (nvidia-smi)</label>
          <label className="lv-check-label"><input type="checkbox" checked={d.host} disabled={locked} onChange={(e: any) => setD({ ...d, host: e.target.checked })} /> Read host vitals (CPU, memory, disk, network)</label>
          <label>Cluster / host label<input className="lv-input wide" value={d.cluster_name} disabled={locked} placeholder={`live@${s.status.hostname}`} onInput={(e: any) => setD({ ...d, cluster_name: e.target.value })} /></label>
          <label>Refresh interval (seconds)
            <select className="scenario-select" disabled={locked} value={d.interval} onChange={(e: any) => setD({ ...d, interval: e.target.value })}>
              {['1', '2', '5', '10', '15', '30', '60'].map((v) => <option value={v} selected={d.interval === v}>{v}s</option>)}
            </select>
          </label>
        </div>
        <div className="lv-toolbar" style={{ marginTop: '12px' }}>
          <button className="btn btn-primary" disabled={locked || busy !== ''} onClick={save}>{busy === 'config' ? 'Saving…' : 'Save & re-check everything'}</button>
          <button className="btn" disabled={busy !== ''} onClick={reload}>Discard changes</button>
          <span className="lv-note">Tokens are write-only: they are never sent back to the browser.</span>
        </div>
      </div>
      <div className="panel">
        <div className="panel-header"><div className="panel-title">Startup-only settings</div></div>
        <table className="lv-kv"><tbody>
          {([
            ['nvidia-smi path', s.config.nvidia_smi_path || '(auto-discovered)'], ['History kept', `${s.config.history_points} samples`],
            ['Idle auto-stop', s.config.idle_timeout_seconds ? duration(s.config.idle_timeout_seconds) : 'never'],
            ['Prometheus backfill', `${s.config.prometheus_backfill_minutes} min`], ['Running in a cluster', s.config.in_cluster ? 'yes (service account)' : 'no'],
            ['Remote config changes', s.config.allow_remote_config ? 'allowed' : 'local machine only'],
          ] as [string, string][]).map(([k, v]) => <tr><td>{k}</td><td className="mono">{v}</td></tr>)}
        </tbody></table>
        <div className="lv-note" style={{ marginTop: '6px' }}>Binary paths, token files and CA files can only be set with flags or GPULENS_* environment variables, never from the browser.</div>
      </div>
    </div>
  );
}

// ---------- Thresholds ----------
const TH_LABELS: Record<string, [string, string]> = {
  temp_warning_celsius: ['GPU temperature — warning', '°C'],
  temp_critical_celsius: ['GPU temperature — critical', '°C'],
  mem_temp_warning_celsius: ['HBM temperature — warning', '°C'],
  fb_memory_warning_percent: ['VRAM pressure — warning', '%'],
  power_cap_warning_percent: ['Power cap — warning', '% of limit'],
  low_util_threshold_percent: ['Low utilization below', '%'],
  low_util_window_minutes: ['Low-utilization observation window', 'min'],
  min_utilization_samples: ['Minimum samples for low-utilization', 'samples'],
  high_vram_context_percent: ['High VRAM context', '%'],
  max_staleness_seconds: ['Telemetry considered stale after', 's'],
  pending_grace_seconds: ['Pending-pod grace period', 's'],
  critical_pending_minutes: ['Pending pod becomes critical after', 'min'],
  xid_error_threshold: ['XID error count', 'count'],
  ecc_dbe_threshold: ['ECC double-bit errors', 'count'],
  row_remap_failure_threshold: ['Row-remap failures', 'count'],
  pcie_replay_warning_count: ['PCIe replay count — warning', 'count'],
  nvlink_crc_error_threshold: ['NVLink CRC errors', 'count'],
  correlation_lag_minutes: ['Failure correlation lag', 'min'],
  correlation_lead_minutes: ['Failure correlation lead', 'min'],
  optional_sample_gpu_hourly_rate_usd: ['Sample GPU hourly rate (cost illustration)', 'USD'],
};

export function renderThresholds(s: LiveState, draft: Record<string, string>, setDraft: (d: Record<string, string>) => void, apply: () => void, reset: () => void, busy: string) {
  const keys = Object.keys(s.thresholds).sort((a, b) => (TH_LABELS[a]?.[0] || a).localeCompare(TH_LABELS[b]?.[0] || b));
  const changed = keys.filter((k) => draft[k] !== undefined && draft[k] !== '' && Number(draft[k]) !== s.thresholds[k]);
  return (
    <div className="panel">
      <div className="panel-header">
        <div className="panel-title">Rule thresholds for this live session</div>
        <span className="lv-note">The simulated demo always uses the defaults; changes here only affect Go Live.</span>
      </div>
      <table className="data-table">
        <thead><tr><th>Threshold</th><th>Default</th><th>Active</th><th>New value</th></tr></thead>
        <tbody>
          {keys.map((k) => {
            const over = s.threshold_overrides.indexOf(k) >= 0;
            return (
              <tr key={k}>
                <td><div>{TH_LABELS[k]?.[0] || k}</div><div className="lv-note mono">{k}</div></td>
                <td className="mono">{s.threshold_defaults[k]}</td>
                <td className="mono">{s.thresholds[k]} {over && <Badge kind="warning">overridden</Badge>}</td>
                <td><input className="lv-input mono" style={{ width: '110px' }} type="number" step="any" placeholder={String(s.thresholds[k])}
                  value={draft[k] ?? ''} onInput={(e: any) => setDraft({ ...draft, [k]: e.target.value })} /> <span className="lv-note">{TH_LABELS[k]?.[1] || ''}</span></td>
              </tr>
            );
          })}
        </tbody>
      </table>
      <div className="lv-toolbar" style={{ marginTop: '12px' }}>
        <button className="btn btn-primary" disabled={changed.length === 0 || busy !== ''} onClick={apply}>Apply {changed.length || ''} change{changed.length === 1 ? '' : 's'}</button>
        <button className="btn" disabled={s.threshold_overrides.length === 0 || busy !== ''} onClick={reset}>Reset all to defaults</button>
        <span className="lv-note">Invalid combinations (for example warning above critical) are rejected by the server.</span>
      </div>
    </div>
  );
}

// ---------- Activity ----------
export function renderActivity(L: LiveStore, s: LiveState, filter: string, setFilter: (f: string) => void) {
  const rows = L.activity.slice().reverse().filter((a) => filter === 'all' || a.severity === filter || a.kind === filter);
  const resolved = s.resolved_incidents || [];
  return (
    <div>
      <div className="panel">
        <div className="panel-header">
          <div className="panel-title">Activity log</div>
          <select className="scenario-select" value={filter} onChange={(e: any) => setFilter(e.target.value)}>
            {[['all', 'Everything'], ['critical', 'Critical'], ['warning', 'Warnings'], ['success', 'Recoveries'], ['source', 'Source changes'], ['incident', 'Incidents']].map(([v, l]) => (
              <option value={v} selected={filter === v}>{l}</option>
            ))}
          </select>
        </div>
        {rows.length === 0 ? <Empty title="Nothing logged yet">Connection changes, incidents opening and resolving, and stale-telemetry events appear here as they happen.</Empty> : (
          rows.slice(0, 300).map((a) => (
            <div className="lv-row" key={a.seq}>
              <span className="mono lv-note" style={{ minWidth: '64px' }}>{clock(a.t)}</span>
              <Badge kind={a.severity === 'critical' ? 'critical' : a.severity === 'warning' ? 'warning' : a.severity === 'success' ? 'healthy' : 'info'}>{a.kind}</Badge>
              <div style={{ flex: '1' }}><div style={{ fontWeight: 600 }}>{a.title}</div>{a.detail && <div className="lv-note">{a.detail}</div>}</div>
            </div>
          ))
        )}
      </div>
      <div className="panel">
        <div className="panel-header"><div className="panel-title">Resolved this session</div><span className="lv-note">{resolved.length}</span></div>
        {resolved.length === 0 ? <Empty title="No incident has resolved yet">Incidents that stop firing are listed here with how long they lasted.</Empty> : (
          resolved.slice().reverse().map((r) => (
            <div className="lv-row" key={r.incident.id + r.resolved_at}>
              <Badge kind="healthy">resolved</Badge>
              <div style={{ flex: '1' }}><div style={{ fontWeight: 600 }}>{r.incident.title}</div>
                <div className="lv-note">{r.incident.rule_id} · lasted {duration(r.duration_seconds)} · resolved at {clock(r.resolved_at)}</div></div>
            </div>
          ))
        )}
      </div>
    </div>
  );
}
