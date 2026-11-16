// Command center, preflight, GPU and host views of the live console.
import { h, Fragment } from '../jsx-runtime';
import { LiveStore } from './store';
import { LiveGPU, LiveState } from './types';
import {
  Badge, CHECK_ICON, Empty, Heatmap, LineChart, Meter, Sparkline, Stat, STATE_LABEL,
  ago, bytesPerSec, clock, duration, mib, num, tone,
} from './ui';

const C = { blue: '#38bdf8', indigo: '#818cf8', green: '#10b981', amber: '#f59e0b', red: '#ef4444', purple: '#a855f7', slate: '#94a3b8' };

function sevKind(s: string): string {
  return s === 'critical' ? 'critical' : s === 'warning' ? 'warning' : s === 'inconclusive' ? 'inconclusive' : 'healthy';
}

export function verdictClass(v: string): string {
  return v === 'ready' ? 'healthy' : v === 'degraded' ? 'warning' : v === 'blocked' ? 'critical' : 'inconclusive';
}

export function renderCommandCenter(L: LiveStore, s: LiveState, go: (tab: string) => void) {
  const ov = s.overview;
  const pts = L.history;
  const times = pts.map((p) => p.t);
  const th = s.thresholds;
  const gpus = s.gpus;
  const fresh = gpus.filter((g) => !g.is_stale).length;
  const counts = ov?.active_incident_counts || { critical: 0, warning: 0, inconclusive: 0 };
  const cap = ov?.capacity_summary;
  const connected = s.sources.filter((x) => x.state === 'connected' || x.state === 'degraded').length;
  const enabled = s.sources.filter((x) => x.enabled).length;
  const latest = pts.length ? pts[pts.length - 1] : null;
  const maxTemp = gpus.reduce<number | null>((m, g) => {
    const t = g.latest.gpu_temp_celsius;
    return t === null || t === undefined ? m : m === null ? t : Math.max(m, t);
  }, null);
  const power = gpus.reduce((a, g) => a + (g.latest.power_usage_watts || 0), 0);
  const havePower = gpus.some((g) => g.latest.power_usage_watts !== null);
  const incidents = ov?.active_incidents || [];

  return (
    <div>
      <div className="lv-stat-grid">
        <Stat label="GPUs monitored" value={gpus.length} sub={`${fresh} fresh · ${gpus.length - fresh} stale`} kind={gpus.length - fresh > 0 ? 'warning' : ''} />
        <Stat label="Fleet utilization" value={num(latest?.util, 1, '%')} sub="mean GPU SM util" />
        <Stat label="VRAM in use" value={num(latest?.fb, 1, '%')} sub="mean framebuffer" />
        <Stat label="Hottest GPU" value={num(maxTemp, 0, '°C')} sub={`warn ${th.temp_warning_celsius}° · crit ${th.temp_critical_celsius}°`} kind={tone(maxTemp, th.temp_warning_celsius, th.temp_critical_celsius)} />
        <Stat label="GPU power draw" value={havePower ? num(power, 0, ' W') : '—'} sub="sum of all GPUs" />
        <Stat label="Open incidents" value={counts.critical + counts.warning + counts.inconclusive} sub={`${counts.critical} critical · ${counts.warning} warning · ${counts.inconclusive} inconclusive`} kind={counts.critical ? 'critical' : counts.warning ? 'warning' : ''} />
        <Stat label="Free allocatable" value={cap ? `${cap.total_free_allocatable_gpus}/${cap.total_allocatable_gpus}` : '—'} sub={cap ? `${cap.pending_requested_gpus} requested by pending pods` : 'needs Kubernetes'} />
        <Stat label="Sources" value={`${connected}/${enabled}`} sub="connected / enabled" kind={connected < enabled ? 'warning' : ''} />
      </div>

      <div className="lv-grid-2">
        <div className="panel">
          <div className="panel-header"><div className="panel-title">Utilization &amp; VRAM</div><span className="lv-note">{pts.length} samples this session</span></div>
          <LineChart times={times} yMax={100} series={[
            { name: 'GPU util %', color: C.blue, values: pts.map((p) => p.util) },
            { name: 'VRAM used %', color: C.indigo, values: pts.map((p) => p.fb) },
          ]} />
        </div>
        <div className="panel">
          <div className="panel-header"><div className="panel-title">Hottest GPU temperature</div><span className="lv-note">°C</span></div>
          <LineChart times={times} yMin={20} series={[{ name: 'max temp', color: C.amber, values: pts.map((p) => p.temp_max) }]}
            threshold={{ value: th.temp_critical_celsius, label: `critical ${th.temp_critical_celsius}°C` }} />
        </div>
        <div className="panel">
          <div className="panel-header"><div className="panel-title">Total GPU power</div><span className="lv-note">watts</span></div>
          <LineChart times={times} series={[{ name: 'power', color: C.green, values: pts.map((p) => p.power) }]} />
        </div>
        <div className="panel">
          <div className="panel-header"><div className="panel-title">Open incidents over time</div><span className="lv-note">count</span></div>
          <LineChart times={times} series={[
            { name: 'critical', color: C.red, values: pts.map((p) => p.crit) },
            { name: 'warning', color: C.amber, values: pts.map((p) => p.warn) },
          ]} />
        </div>
      </div>

      <div className="panel">
        <div className="panel-header">
          <div className="panel-title">Per-GPU utilization heat map</div>
          <span className="lv-note">one row per GPU · blue = idle → red = saturated</span>
        </div>
        <Heatmap rows={gpus.map((g) => ({ id: g.uuid, label: `${g.node_name.length > 18 ? '…' + g.node_name.slice(-17) : g.node_name}/gpu${g.device_index}` }))} points={pts} idx={0} max={100} />
      </div>

      <div className="lv-grid-2">
        <div className="panel">
          <div className="panel-header">
            <div className="panel-title">Active incidents</div>
            <button className="btn" onClick={() => go('incidents')}>Open incident queue</button>
          </div>
          {incidents.length === 0 ? (
            <Empty title={gpus.length === 0 ? 'No GPU telemetry yet' : 'No rule is firing'}>
              {gpus.length === 0
                ? 'Nothing can be called healthy until a source reports GPUs. See Check Everything.'
                : 'All six deterministic rules evaluated this cycle with the thresholds shown in the Thresholds tab.'}
            </Empty>
          ) : (
            incidents.slice(0, 6).map((inc: any) => (
              <div className="lv-row" key={inc.id}>
                <Badge kind={sevKind(inc.severity)}>{inc.severity}</Badge>
                <div style={{ flex: '1' }}>
                  <div style={{ fontWeight: 600 }}>{inc.title}</div>
                  <div className="lv-note mono">{inc.rule_id}</div>
                </div>
              </div>
            ))
          )}
        </div>
        <div className="panel">
          <div className="panel-header">
            <div className="panel-title">Sources at a glance</div>
            <button className="btn" onClick={() => go('sources')}>Details</button>
          </div>
          {s.sources.map((src) => (
            <div className="lv-row" key={src.id}>
              <span className={`lv-dot ${src.state}`} />
              <div style={{ flex: '1' }}>
                <div style={{ fontWeight: 600 }}>{src.name}</div>
                <div className="lv-note">{src.detail}</div>
              </div>
              <Badge kind={stateKind(src.state)}>{STATE_LABEL[src.state] || src.state}</Badge>
            </div>
          ))}
        </div>
      </div>
    </div>
  );
}

export function stateKind(st: string): string {
  switch (st) {
    case 'connected': return 'healthy';
    case 'degraded': return 'warning';
    case 'unreachable':
    case 'error': return 'critical';
    case 'not_found': return 'inconclusive';
    default: return 'info';
  }
}

export function renderPreflight(L: LiveStore, s: LiveState, recheck: () => void) {
  const pf = s.preflight;
  const sum = pf.summary || {};
  const now = Date.now();
  return (
    <div>
      <div className="lv-stat-grid">
        <Stat label="Passed" value={sum.pass || 0} kind="" />
        <Stat label="Warnings" value={sum.warn || 0} kind={sum.warn ? 'warning' : ''} />
        <Stat label="Failed" value={sum.fail || 0} kind={sum.fail ? 'critical' : ''} />
        <Stat label="Notes / skipped" value={`${sum.info || 0} / ${sum.skip || 0}`} sub="skipped = needs a source that is not connected" />
      </div>
      <div className="lv-toolbar">
        <button className="btn btn-primary" disabled={L.busy !== ''} onClick={recheck}>
          {L.busy === 'recheck' ? 'Checking…' : 'Re-check everything now'}
        </button>
        <span className="lv-note">Evaluated {ago(pf.generated_at, now)} from live collector state. Checks marked “skipped” need a source that is not connected.</span>
      </div>
      {pf.groups.map((g) => (
        <div className="panel" key={g.id}>
          <div className="panel-header">
            <div className="panel-title">{g.title}</div>
            <span className="lv-note">
              {g.checks.filter((c) => c.status === 'pass').length}/{g.checks.length} passed
            </span>
          </div>
          {g.checks.map((c) => (
            <div className={`lv-check ${c.status}`} key={c.id}>
              <span className={`lv-check-icon ${c.status}`}>{CHECK_ICON[c.status] || '?'}</span>
              <div style={{ flex: '1' }}>
                <div style={{ fontWeight: 600 }}>
                  {c.title}
                  {c.latency_ms !== null && c.latency_ms !== undefined && <span className="lv-note mono"> · {c.latency_ms.toFixed(0)} ms</span>}
                </div>
                <div className="lv-note">{c.detail}</div>
                {c.hint && c.status !== 'pass' && <div className="lv-hint">→ {c.hint}</div>}
              </div>
              <span className="lv-check-status">{c.status}</span>
            </div>
          ))}
        </div>
      ))}
    </div>
  );
}

export interface GPUFilters {
  q: string;
  node: string;
  sort: string;
  status: string;
}

function gpuHist(L: LiveStore, uuid: string, idx: number): (number | null)[] {
  return L.history.slice(-90).map((p) => (p.g[uuid] ? p.g[uuid][idx] : null));
}

function gpuMatches(g: LiveGPU, f: GPUFilters): boolean {
  if (f.node !== 'all' && g.node_name !== f.node) return false;
  if (f.status === 'issues' && !g.worst_severity) return false;
  if (f.status === 'stale' && !g.is_stale) return false;
  if (f.status === 'idle' && !((g.latest.gpu_util_percent ?? 100) < 5)) return false;
  if (f.q.trim()) {
    const q = f.q.toLowerCase();
    const hay = `${g.uuid} ${g.node_name} ${g.model_name} ${g.allocated_pod} ${g.allocated_namespace} gpu${g.device_index}`.toLowerCase();
    if (!hay.includes(q)) return false;
  }
  return true;
}

const SEV_ORDER: Record<string, number> = { critical: 0, warning: 1, inconclusive: 2, info: 3, '': 4 };

export function renderGPUs(
  L: LiveStore, s: LiveState, f: GPUFilters, setF: (f: GPUFilters) => void,
  openUUID: string, setOpen: (u: string) => void
) {
  const nodes = Array.from(new Set(s.gpus.map((g) => g.node_name))).sort();
  let list = s.gpus.filter((g) => gpuMatches(g, f));
  const val = (g: LiveGPU) => {
    switch (f.sort) {
      case 'util': return -(g.latest.gpu_util_percent ?? -1);
      case 'temp': return -(g.latest.gpu_temp_celsius ?? -1);
      case 'vram': return -(g.fb_used_percent ?? -1);
      case 'power': return -(g.latest.power_usage_watts ?? -1);
      case 'severity': return SEV_ORDER[g.worst_severity || ''] ?? 4;
      default: return 0;
    }
  };
  list = list.slice().sort((a, b) => val(a) - val(b) || a.node_name.localeCompare(b.node_name) || a.device_index - b.device_index);
  const th = s.thresholds;

  if (s.gpus.length === 0) {
    return (
      <div className="panel">
        <Empty title="No GPUs reported yet">
          GPU Lens has not received telemetry from any source. Open <b>Check Everything</b> to see what is missing, or start the Live Lab
          emulator (<span className="mono">python3 scripts/live_lab.py</span>) to try the console without hardware.
        </Empty>
      </div>
    );
  }

  return (
    <div>
      <div className="lv-toolbar">
        <input className="lv-input" placeholder="Search GPU, node, pod, UUID…" value={f.q} onInput={(e: any) => setF({ ...f, q: e.target.value })} />
        <select className="scenario-select" value={f.node} onChange={(e: any) => setF({ ...f, node: e.target.value })}>
          <option value="all" selected={f.node === 'all'}>All nodes</option>
          {nodes.map((n) => <option value={n} selected={f.node === n}>{n}</option>)}
        </select>
        <select className="scenario-select" value={f.status} onChange={(e: any) => setF({ ...f, status: e.target.value })}>
          {[['all', 'All states'], ['issues', 'With incidents'], ['stale', 'Stale telemetry'], ['idle', 'Idle (<5% util)']].map(([v, l]) => (
            <option value={v} selected={f.status === v}>{l}</option>
          ))}
        </select>
        <select className="scenario-select" value={f.sort} onChange={(e: any) => setF({ ...f, sort: e.target.value })}>
          {[['index', 'Sort: node / index'], ['severity', 'Sort: severity'], ['util', 'Sort: utilization'], ['temp', 'Sort: temperature'], ['vram', 'Sort: VRAM'], ['power', 'Sort: power']].map(([v, l]) => (
            <option value={v} selected={f.sort === v}>{l}</option>
          ))}
        </select>
        <span className="lv-note">{list.length} of {s.gpus.length} GPUs</span>
      </div>
      <div className="lv-gpu-grid">
        {list.map((g) => {
          const m = g.latest;
          const open = openUUID === g.uuid;
          const pctPower = m.power_usage_watts !== null && g.power_limit_watts > 0 ? (m.power_usage_watts / g.power_limit_watts) * 100 : null;
          return (
            <div className={`lv-gpu ${g.worst_severity || ''} ${g.is_stale ? 'stale' : ''}`} key={g.uuid}>
              <div className="lv-gpu-head">
                <div>
                  <div style={{ fontWeight: 700 }}>{g.node_name} · GPU {g.device_index}</div>
                  <div className="lv-note">{g.model_name || 'Unknown model'}{g.mig_profile ? ` · MIG ${g.mig_profile}` : ''}</div>
                </div>
                <div className="lv-badges">
                  {g.worst_severity && <Badge kind={sevKind(g.worst_severity)}>{g.worst_severity}</Badge>}
                  {g.is_stale && <Badge kind="inconclusive">stale {duration(g.staleness_seconds)}</Badge>}
                  {g.emulated && <Badge kind="info">emulated</Badge>}
                </div>
              </div>
              <div className="lv-workload">
                {g.allocated_pod
                  ? <span><b>{g.allocated_namespace}/{g.allocated_pod}</b>{g.allocated_container ? ` · ${g.allocated_container}` : ''}</span>
                  : <span className="lv-note">{g.mapping_status === 'mapped' ? 'No workload assigned' : `Workload mapping: ${g.mapping_status || 'unknown'}`}</span>}
              </div>
              <div className="lv-metric"><span>Utilization</span><b>{num(m.gpu_util_percent, 0, '%')}</b><Meter value={m.gpu_util_percent} /><Sparkline values={gpuHist(L, g.uuid, 0)} max={100} color={C.blue} /></div>
              <div className="lv-metric"><span>VRAM</span><b>{num(g.fb_used_percent, 0, '%')}</b><Meter value={g.fb_used_percent} warn={th.fb_memory_warning_percent} crit={101} /><Sparkline values={gpuHist(L, g.uuid, 1)} max={100} color={C.indigo} /></div>
              <div className="lv-metric"><span>Temperature</span><b>{num(m.gpu_temp_celsius, 0, '°C')}</b><Meter value={m.gpu_temp_celsius} max={100} warn={th.temp_warning_celsius} crit={th.temp_critical_celsius} /><Sparkline values={gpuHist(L, g.uuid, 2)} color={C.amber} /></div>
              <div className="lv-metric"><span>Power</span><b>{num(m.power_usage_watts, 0, 'W')}</b><Meter value={pctPower} warn={th.power_cap_warning_percent} crit={101} /><Sparkline values={gpuHist(L, g.uuid, 3)} color={C.green} /></div>
              {g.throttle_reasons && g.throttle_reasons.length > 0 && (
                <div className="lv-throttle">Throttling: {g.throttle_reasons.join(', ')}</div>
              )}
              <div className="lv-gpu-foot">
                <span className="lv-note mono">{g.uuid.slice(0, 22)}…</span>
                <button className="btn" onClick={() => setOpen(open ? '' : g.uuid)}>{open ? 'Hide details' : 'Details'}</button>
              </div>
              {open && renderGPUDetail(g)}
            </div>
          );
        })}
      </div>
    </div>
  );
}

function renderGPUDetail(g: LiveGPU) {
  const m = g.latest as any;
  const rows: [string, any][] = [
    ['UUID', g.uuid], ['PCI bus', g.pci_bus_id || '—'], ['Driver', g.driver_version || '—'], ['P-state', g.pstate || '—'],
    ['Memory total', mib(g.total_memory_mib || null)], ['VRAM used / free', `${mib(m.fb_used_mib)} / ${mib(m.fb_free_mib)}`],
    ['Memory copy util', num(m.mem_copy_util_percent, 0, '%')], ['Memory (HBM) temp', num(m.memory_temp_celsius, 0, '°C')],
    ['Power limit', g.power_limit_watts ? `${g.power_limit_watts} W` : '—'], ['Fan', g.fan_speed_percent === null ? '—' : `${g.fan_speed_percent}%`],
    ['PCIe link', g.pcie ? `Gen${g.pcie.gen_current}/${g.pcie.gen_max} · x${g.pcie.width_current}/${g.pcie.width_max}` : '—'],
    ['XID error', m.xid_error_code === null ? '— (not exported)' : m.xid_error_code], ['PCIe replays', m.pcie_replay_counter === null ? '— (not exported)' : m.pcie_replay_counter],
    ['ECC double-bit', m.ecc_dbe_vol_total === null ? '— (not exported)' : m.ecc_dbe_vol_total], ['ECC single-bit', m.ecc_sbe_vol_total === null ? '— (not exported)' : m.ecc_sbe_vol_total],
    ['Row-remap failure', m.row_remap_failure === null ? '— (not exported)' : m.row_remap_failure], ['Thermal violation', m.thermal_violation_ns === null ? '— (not exported)' : `${m.thermal_violation_ns} ns`],
    ['Reported by', (g.sources || []).join(', ') || '—'], ['Samples this session', g.sample_count], ['Incidents', (g.incident_ids || []).join(', ') || 'none'],
  ];
  return (
    <div className="lv-detail">
      <table className="lv-kv">
        <tbody>{rows.map(([k, v]) => <tr><td>{k}</td><td className="mono">{v}</td></tr>)}</tbody>
      </table>
      {g.processes && g.processes.length > 0 && (
        <div style={{ marginTop: '8px' }}>
          <div className="lv-note" style={{ marginBottom: '4px' }}>Compute processes</div>
          {g.processes.map((p) => <div className="mono" style={{ fontSize: '12px' }}>PID {p.pid} · {p.name} · {mib(p.used_memory_mib)}</div>)}
        </div>
      )}
      <div className="lv-note" style={{ marginTop: '6px' }}>“— (not exported)” means the source does not publish that metric; GPU Lens never shows a missing metric as zero.</div>
    </div>
  );
}

export function renderHost(L: LiveStore, s: LiveState) {
  const host = s.host;
  if (!host || !host.supported) {
    return (
      <div className="panel">
        <Empty title="Host vitals are not available">
          {s.config.host_vitals_enabled
            ? 'This platform does not expose /proc and /sys (host vitals need Linux). GPU telemetry is unaffected.'
            : 'Host vitals are switched off in Connections.'}
        </Empty>
      </div>
    );
  }
  const pts = L.history;
  const times = pts.map((p) => p.t);
  const cpus = host.logical_cpus || 1;
  return (
    <div>
      <div className="lv-stat-grid">
        <Stat label="CPU" value={num(host.cpu_percent, 1, '%')} sub={`${cpus} logical CPUs · iowait ${num(host.cpu_iowait_percent, 1, '%')}`} kind={tone(host.cpu_percent, 85, 95)} />
        <Stat label="Load (1/5/15 min)" value={`${num(host.load1, 2)} / ${num(host.load5, 2)} / ${num(host.load15, 2)}`} sub={host.load1 !== null && host.load1 > cpus ? 'above CPU count' : 'within CPU count'} kind={host.load1 !== null && host.load1 > cpus ? 'warning' : ''} />
        <Stat label="Memory" value={num(host.mem_used_percent, 1, '%')} sub={`${mib(host.mem_available_mib)} available of ${mib(host.mem_total_mib)}`} kind={tone(host.mem_used_percent, 85, 95)} />
        <Stat label="Swap" value={host.swap_total_mib ? mib(host.swap_used_mib) : 'none'} sub={host.swap_total_mib ? `of ${mib(host.swap_total_mib)}` : 'no swap configured'} />
        <Stat label={`Disk ${host.disk_path || ''}`} value={num(host.disk_used_percent, 1, '%')} sub={host.disk_total_gib !== null ? `${num(host.disk_total_gib, 0)} GiB total` : ''} kind={tone(host.disk_used_percent, 85, 95)} />
        <Stat label="Network" value={`↓ ${bytesPerSec(host.net_rx_bytes_per_sec)}`} sub={`↑ ${bytesPerSec(host.net_tx_bytes_per_sec)}`} />
        <Stat label="Uptime" value={duration(host.uptime_seconds)} sub={host.in_container ? 'inside a container' : 'bare metal / VM'} />
        <Stat label="NVIDIA driver" value={host.nvidia_driver_version || 'not detected'} sub={`${host.pci_gpus.length} NVIDIA PCI device(s)`} />
      </div>
      <div className="lv-grid-2">
        <div className="panel">
          <div className="panel-header"><div className="panel-title">Host CPU %</div></div>
          <LineChart times={times} yMax={100} series={[{ name: 'CPU', color: C.blue, values: pts.map((p) => p.cpu) }]} height={150} />
        </div>
        <div className="panel">
          <div className="panel-header"><div className="panel-title">Host memory %</div></div>
          <LineChart times={times} yMax={100} series={[{ name: 'Memory', color: C.purple, values: pts.map((p) => p.mem) }]} height={150} />
        </div>
      </div>
      <div className="lv-grid-2">
        <div className="panel">
          <div className="panel-header"><div className="panel-title">System</div></div>
          <table className="lv-kv"><tbody>
            {([
              ['Hostname', host.hostname], ['OS', `${host.platform || host.os} (${host.arch})`], ['Kernel', host.kernel || '—'], ['CPU model', host.cpu_model || '—'],
              ['Pressure (PSI, 10 s)', host.pressure ? `cpu ${num(host.pressure.cpu_some_avg10, 2)} · mem ${num(host.pressure.memory_some_avg10, 2)} · io ${num(host.pressure.io_some_avg10, 2)}` : 'not available'],
            ] as [string, any][]).map(([k, v]) => <tr><td>{k}</td><td className="mono">{v}</td></tr>)}
          </tbody></table>
        </div>
        <div className="panel">
          <div className="panel-header"><div className="panel-title">GPU Lens process</div></div>
          <table className="lv-kv"><tbody>
            {([
              ['PID', host.process.pid], ['Runtime', host.process.runtime], ['Uptime', duration(host.process.uptime_seconds)],
              ['Memory', mib(host.process.memory_mib)], ['Workers', host.process.workers],
            ] as [string, any][]).map(([k, v]) => <tr><td>{k}</td><td className="mono">{v}</td></tr>)}
          </tbody></table>
        </div>
      </div>
      {host.pci_gpus.length > 0 && (
        <div className="panel">
          <div className="panel-header"><div className="panel-title">NVIDIA devices on the PCI bus</div></div>
          <table className="data-table">
            <thead><tr><th>Address</th><th>Device ID</th><th>Class</th><th>Kernel driver</th></tr></thead>
            <tbody>{host.pci_gpus.map((d) => <tr><td className="mono">{d.address}</td><td className="mono">{d.vendor_id}:{d.device_id}</td><td>{d.class}</td><td className="mono">{d.driver || 'none bound'}</td></tr>)}</tbody>
          </table>
        </div>
      )}
      {host.notes && host.notes.length > 0 && <div className="panel">{host.notes.map((n) => <div className="lv-note">• {n}</div>)}</div>}
    </div>
  );
}
