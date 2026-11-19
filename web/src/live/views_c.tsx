// Go Live console, part 3: fleet (multi-cluster), alerting and durable-state panels.
import { h, Fragment } from '../jsx-runtime';
import { LiveStore } from './store';
import { FleetMember, LiveState } from './types';
import { ago, Badge, Empty, num, Stat } from './ui';
import { verdictClass } from './views_a';

function memberLink(m: FleetMember): string {
  if (m.kind !== 'peer') return '';
  // Credentials are redacted as ***@ by the server; the link is the bare console address.
  return m.url.replace(/\/\/\*\*\*@/, '//') + '/';
}

export function renderFleet(L: LiveStore) {
  const f = L.fleet;
  if (!f) {
    return <div className="panel"><Empty title="Loading the fleet view…">Asking every GPU Lens peer for its summary.</Empty></div>;
  }
  const t = f.totals;
  const now = Date.now();
  return (
    <div>
      <div className="lv-stat-grid">
        <Stat label="Clusters / hosts" value={`${t.reachable}/${t.members}`} sub="reachable / configured" kind={t.reachable < t.members ? 'warning' : ''} />
        <Stat label="GPUs" value={t.gpus_total} sub={`${t.gpus_fresh} reporting fresh telemetry`} />
        <Stat label="Critical incidents" value={t.critical} sub="across every reachable member" kind={t.critical ? 'critical' : ''} />
        <Stat label="Warnings" value={t.warning} sub={`${t.info} informational`} kind={t.warning ? 'warning' : ''} />
        <Stat label="Need attention" value={t.unhealthy} sub="unreachable, blocked or critical" kind={t.unhealthy ? 'critical' : ''} />
      </div>
      <div className="panel">
        <div className="panel-header">
          <div className="panel-title">Fleet — every GPU Lens instance</div>
          <span className="lv-note">refreshed every {f.interval_seconds}s</span>
        </div>
        <div className="lv-note" style={{ marginBottom: '10px' }}>{f.note}</div>
        <div className="lv-fleet-grid">
          {f.members.map((m) => {
            const s = m.summary;
            const crit = s ? s.incidents.critical || 0 : 0;
            const warn = s ? s.incidents.warning || 0 : 0;
            const link = memberLink(m);
            return (
              <div className={`lv-fleet-card ${!m.reachable ? 'down' : crit ? 'crit' : warn ? 'warn' : ''}`} key={m.kind + m.name}>
                <div className="lv-fleet-head">
                  <div>
                    <div className="lv-fleet-name">{s ? s.cluster : m.name}</div>
                    <div className="lv-note mono">{m.kind === 'local' ? 'this instance' : `${m.name} · ${m.url}`}</div>
                  </div>
                  <div style={{ display: 'flex', gap: '6px', flexWrap: 'wrap', justifyContent: 'flex-end' }}>
                    {m.kind === 'local' && <Badge kind="info">local</Badge>}
                    {s && s.emulated && <Badge kind="info">emulated</Badge>}
                    {m.reachable && s ? <span className={`badge ${verdictClass(s.verdict)}`}>{s.verdict}</span> : <Badge kind="critical">unreachable</Badge>}
                  </div>
                </div>
                {m.reachable && s ? (
                  <Fragment>
                    <div className="lv-fleet-stats">
                      <div><b>{s.gpus_fresh}/{s.gpus_total}</b><span>GPUs fresh</span></div>
                      <div><b className={crit ? 'crit' : ''}>{crit}</b><span>critical</span></div>
                      <div><b className={warn ? 'warn' : ''}>{warn}</b><span>warning</span></div>
                      <div><b>{s.sources_connected}/{s.sources_enabled}</b><span>sources</span></div>
                    </div>
                    {s.top_incidents.length > 0
                      ? <ul className="lv-fleet-incidents">{s.top_incidents.map((x) => <li>{x}</li>)}</ul>
                      : <div className="lv-note">{s.gpus_total ? 'No open incidents.' : 'No GPU telemetry on this member — not the same as healthy.'}</div>}
                    <div className="lv-note mono" style={{ marginTop: '6px' }}>
                      {s.runtime} v{s.version} · {s.state} · last cycle {s.last_cycle_at ? ago(s.last_cycle_at, now) : 'never'}
                      {m.kind === 'peer' ? ` · ${num(m.latency_ms, 0)} ms` : ''}
                    </div>
                  </Fragment>
                ) : (
                  <div className="blindspot-box" style={{ marginTop: '8px' }}>
                    <strong>No summary from this member.</strong> {m.error || 'Not polled yet.'}
                    {m.last_ok_at && <span> Last answered {ago(m.last_ok_at, now)}.</span>}
                  </div>
                )}
                {link && <a className="btn" style={{ marginTop: '8px', display: 'inline-block', textDecoration: 'none' }} href={link} target="_blank" rel="noopener noreferrer">Open its console ↗</a>}
              </div>
            );
          })}
        </div>
      </div>
    </div>
  );
}

export function renderAlerting(s: LiveState, canOperate: boolean, busy: string, sendTest: () => void) {
  const a = s.alerts;
  if (!a) return null;
  return (
    <div className="panel">
      <div className="panel-header">
        <div className="panel-title">Alerting</div>
        {a.enabled ? <Badge kind="healthy">on · {a.min_severity}+</Badge> : <Badge kind="info">off</Badge>}
      </div>
      <div className="lv-note" style={{ marginBottom: '8px' }}>{a.note}</div>
      {a.enabled && (
        <table className="data-table">
          <thead><tr><th>Destination</th><th>Sent</th><th>Failed</th><th>Last delivery</th><th>Last error</th></tr></thead>
          <tbody>
            {a.destinations.map((d) => (
              <tr key={d.kind}>
                <td><b>{d.kind === 'slack' ? 'Slack' : 'Webhook'}</b> <span className="lv-note mono">{d.target}</span> {d.signed && <Badge kind="info">signed</Badge>}</td>
                <td className="mono">{d.sent}</td>
                <td className="mono">{d.failed}{d.dropped ? ` (+${d.dropped} dropped)` : ''}</td>
                <td className="mono">{d.last_success_at ? `${ago(d.last_success_at, Date.now())} (${d.last_event})` : 'never'}</td>
                <td className="lv-note">{d.last_error || '—'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      <div className="lv-toolbar" style={{ marginTop: '10px' }}>
        <button className="btn" disabled={!a.enabled || !canOperate || busy !== ''} onClick={sendTest}>{busy === 'alerts' ? 'Sending…' : 'Send a test alert'}</button>
        <span className="lv-note">
          {a.enabled
            ? `Tracking ${a.tracked_incidents} open incident(s). Alert destinations can only be changed at startup, never from the browser.`
            : 'Flags: -alert-slack-webhook, -alert-webhook-url, -alert-webhook-secret-file, -alert-min-severity, -public-url (see docs/ALERTING.md).'}
        </span>
      </div>
    </div>
  );
}

export function renderPersistence(s: LiveState) {
  const p = s.persistence;
  if (!p) return null;
  const now = Date.now();
  return (
    <div className="panel">
      <div className="panel-header">
        <div className="panel-title">Durable live state</div>
        {p.enabled ? (p.last_error ? <Badge kind="warning">save failing</Badge> : <Badge kind="healthy">on</Badge>) : <Badge kind="info">memory only</Badge>}
      </div>
      <div className="lv-note">{p.note}</div>
      {p.enabled && (
        <table className="lv-kv" style={{ marginTop: '8px' }}><tbody>
          <tr><td>File</td><td className="mono">{p.path}</td></tr>
          <tr><td>Restored at startup</td><td className="mono">{p.loaded_at ? ago(p.loaded_at, now) : 'no earlier state'}</td></tr>
          <tr><td>Last saved</td><td className="mono">{p.saved_at ? ago(p.saved_at, now) : 'not yet'}</td></tr>
          {p.last_error && <tr><td>Last error</td><td className="mono">{p.last_error}</td></tr>}
        </tbody></table>
      )}
    </div>
  );
}

export function viewerBanner(s: LiveState) {
  if (s.status.can_operate !== false) return null;
  return (
    <div className="lv-alert info">
      <span>
        <b>Read-only access.</b> You are signed in as <span className="mono">{s.status.user || 'viewer'}</span> with the viewer role: you can watch everything,
        but pausing collection, re-checking, thresholds, connections and test alerts need an admin.
      </span>
    </div>
  );
}
