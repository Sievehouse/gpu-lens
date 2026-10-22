import { h, Fragment, useState, useEffect } from './jsx-runtime';
import {
  OverviewResponse,
  Incident,
  RuleDefinition,
  ScenarioSummary,
  UtilizationHistoryPoint,
} from './types';
import {
  fetchScenarios,
  selectScenario,
  fetchOverview,
  fetchRules,
  fetchSources,
} from './api';

type TabKey = 'overview' | 'incidents' | 'incident_detail' | 'capacity' | 'sources' | 'rules';

function getInitialQueryParam(name: string, fallback: string): string {
  try {
    const sp = new URLSearchParams(window.location.search);
    return sp.get(name) || fallback;
  } catch {
    return fallback;
  }
}

export function App() {
  const initialTab = getInitialQueryParam('tab', 'overview') as TabKey;
  const initialScenarioParam = getInitialQueryParam('scenario', '');

  const [activeTab, setActiveTab] = useState<TabKey>(initialTab);
  const [scenarios, setScenarios] = useState<ScenarioSummary[]>([]);
  const [selectedScenarioId, setSelectedScenarioId] = useState<string>(initialScenarioParam);
  const [overview, setOverview] = useState<OverviewResponse | null>(null);
  const [rulesList, setRulesList] = useState<RuleDefinition[]>([]);
  const [sourcesDetail, setSourcesDetail] = useState<any | null>(null);
  const [selectedIncident, setSelectedIncident] = useState<Incident | null>(null);
  const [loading, setLoading] = useState<boolean>(true);
  const [errorMsg, setErrorMsg] = useState<string>('');

  // Incident list filters
  const [severityFilter, setSeverityFilter] = useState<string>('all');
  const [categoryFilter, setCategoryFilter] = useState<string>('all');
  const [searchQuery, setSearchQuery] = useState<string>('');

  useEffect(() => {
    loadAllData(initialScenarioParam);
  }, []);

  async function loadAllData(targetScenario?: string) {
    setLoading(true);
    setErrorMsg('');
    try {
      let ov: OverviewResponse;
      if (targetScenario) {
        ov = await selectScenario(targetScenario);
      } else {
        ov = await fetchOverview();
      }
      const [scenResp, rulesResp, srcResp] = await Promise.all([
        fetchScenarios(),
        fetchRules(),
        fetchSources(),
      ]);
      setOverview(ov);
      setScenarios(scenResp.scenarios || []);
      setSelectedScenarioId(ov.scenario_id);
      setRulesList(rulesResp.rules || []);
      setSourcesDetail(srcResp);
      if (ov.active_incidents && ov.active_incidents.length > 0) {
        setSelectedIncident(ov.active_incidents[0]);
      } else {
        setSelectedIncident(null);
      }
    } catch (err: any) {
      setErrorMsg(err?.message || 'Failed to load cluster diagnostics from backend API.');
    } finally {
      setLoading(false);
    }
  }

  async function handleScenarioChange(ev: Event) {
    const target = ev.target as HTMLSelectElement;
    const nextId = target.value;
    setSelectedScenarioId(nextId);
    await loadAllData(nextId);
  }

  function openIncidentDetail(inc: Incident) {
    setSelectedIncident(inc);
    setActiveTab('incident_detail');
  }

  if (loading && !overview) {
    return (
      <div className="main-container">
        <div className="panel">
          <div className="panel-title">Loading GPU Lens Diagnostics &amp; Telemetry...</div>
          <p style={{ color: 'var(--text-secondary)', marginTop: '8px' }}>
            Evaluating normalized cluster snapshot against deterministic rule registry.
          </p>
        </div>
      </div>
    );
  }

  if (errorMsg) {
    return (
      <div className="main-container">
        <div className="panel" style={{ borderColor: 'var(--status-critical)' }}>
          <div className="panel-title" style={{ color: '#fca5a5' }}>Backend Diagnostic API Error</div>
          <p style={{ marginTop: '8px' }} className="mono">{errorMsg}</p>
          <button className="btn btn-primary" style={{ marginTop: '12px' }} onClick={() => loadAllData()}>
            Retry Collection
          </button>
        </div>
      </div>
    );
  }

  if (!overview) {
    return <div className="main-container">No cluster snapshot available.</div>;
  }

  const cap = overview.capacity_summary;
  const incidents = overview.active_incidents || [];
  const filteredIncidents = incidents.filter((inc) => {
    if (severityFilter !== 'all' && inc.severity !== severityFilter) return false;
    if (categoryFilter !== 'all' && inc.category !== categoryFilter) return false;
    if (searchQuery.trim() !== '') {
      const q = searchQuery.toLowerCase();
      const matchTitle = inc.title.toLowerCase().includes(q);
      const matchSummary = inc.summary.toLowerCase().includes(q);
      const matchRule = inc.rule_id.toLowerCase().includes(q);
      const matchRes = (inc.affected_resources || []).some((r) => r.toLowerCase().includes(q));
      if (!matchTitle && !matchSummary && !matchRule && !matchRes) return false;
    }
    return true;
  });

  const isSimulated = overview.data_mode === 'simulated_fixture';

  return (
    <div>
      {/* Top Banner Visibly Distinguishing Simulated Fixture Data vs Live Telemetry */}
      <div className="sim-banner">
        <div style={{ display: 'flex', alignItems: 'center', gap: '12px', flexWrap: 'wrap' }}>
          <span className="sim-badge">
            {isSimulated ? 'SIMULATED OFFLINE FIXTURE DATA' : 'LIVE INFRASTRUCTURE TELEMETRY'}
          </span>
          <span style={{ color: '#cbd5e1' }}>
            {isSimulated
              ? 'Deterministic offline demonstration — zero AI APIs, zero GPU hardware required. All findings are produced by explicit rule evaluation.'
              : 'Connected to read-only Kubernetes & Prometheus endpoints.'}
          </span>
        </div>
        <div className="scenario-bar">
          <label style={{ color: '#93c5fd', fontWeight: 600, fontSize: '12px' }}>
            Active Scenario:
          </label>
          <select
            className="scenario-select"
            value={selectedScenarioId}
            onChange={handleScenarioChange}
          >
            {scenarios.map((s) => (
              <option key={s.id} value={s.id} selected={s.id === selectedScenarioId}>
                {s.name}
              </option>
            ))}
          </select>
        </div>
      </div>

      {/* Main Navigation Header */}
      <header className="top-header">
        <div className="brand-group">
          <div className="brand-logo">GL</div>
          <div>
            <div className="brand-title">GPU Lens</div>
            <div className="brand-subtitle">
              GPU Fleet Reliability &amp; Capacity Intelligence · Cluster:{' '}
              <span className="mono" style={{ color: '#e2e8f0' }}>{overview.cluster_name}</span>
            </div>
          </div>
        </div>

        <nav className="nav-tabs">
          <button
            className={`nav-tab ${activeTab === 'overview' ? 'active' : ''}`}
            onClick={() => setActiveTab('overview')}
          >
            Overview
          </button>
          <button
            className={`nav-tab ${activeTab === 'incidents' ? 'active' : ''}`}
            onClick={() => setActiveTab('incidents')}
          >
            Incidents <span className="tab-count">{incidents.length}</span>
          </button>
          <button
            className={`nav-tab ${activeTab === 'incident_detail' ? 'active' : ''}`}
            onClick={() => setActiveTab('incident_detail')}
          >
            Incident Detail
          </button>
          <button
            className={`nav-tab ${activeTab === 'capacity' ? 'active' : ''}`}
            onClick={() => setActiveTab('capacity')}
          >
            Capacity &amp; Utilization
          </button>
          <button
            className={`nav-tab ${activeTab === 'sources' ? 'active' : ''}`}
            onClick={() => setActiveTab('sources')}
          >
            Data Sources <span className="tab-count">{(overview.sources || []).length}</span>
          </button>
          <button
            className={`nav-tab ${activeTab === 'rules' ? 'active' : ''}`}
            onClick={() => setActiveTab('rules')}
          >
            Rules Registry <span className="tab-count">{rulesList.length}</span>
          </button>
        </nav>
      </header>

      <main className="main-container">
        {/* Scenario Context Card */}
        <div className="scenario-context-card">
          <div>
            <div style={{ fontWeight: 700, fontSize: '14px', color: '#f8fafc' }}>
              {overview.scenario_name}
            </div>
            <div style={{ color: 'var(--text-secondary)', fontSize: '13px', marginTop: '2px' }}>
              {overview.scenario_description}
            </div>
          </div>
          <div style={{ textAlign: 'right', minWidth: '210px' }}>
            <div className="mono" style={{ fontSize: '11.5px', color: 'var(--text-secondary)' }}>
              Snapshot Time: {overview.collected_at}
            </div>
            <div style={{ marginTop: '4px' }}>
              <span className="badge healthy">Read-Only Enforced</span>
            </div>
          </div>
        </div>

        {/* Fleet Status Banner */}
        <div className={`health-banner ${overview.fleet_health_status}`}>
          <div>
            <div style={{ display: 'flex', alignItems: 'center', gap: '10px' }}>
              <span className={`badge ${overview.fleet_health_status}`}>
                {overview.fleet_health_status.replace(/_/g, ' ')}
              </span>
              <strong style={{ fontSize: '14.5px' }}>{overview.fleet_health_headline}</strong>
            </div>
          </div>
          <div className="mono" style={{ fontSize: '12px' }}>
            Critical: {overview.active_incident_counts?.critical || 0} · Warning:{' '}
            {overview.active_incident_counts?.warning || 0} · Inconclusive:{' '}
            {overview.active_incident_counts?.inconclusive || 0}
          </div>
        </div>

        {activeTab === 'overview' && renderOverviewTab(overview, cap, incidents, openIncidentDetail, setActiveTab)}
        {activeTab === 'incidents' &&
          renderIncidentsTab(
            filteredIncidents,
            severityFilter,
            setSeverityFilter,
            categoryFilter,
            setCategoryFilter,
            searchQuery,
            setSearchQuery,
            openIncidentDetail
          )}
        {activeTab === 'incident_detail' &&
          renderIncidentDetailTab(selectedIncident, incidents, openIncidentDetail)}
        {activeTab === 'capacity' && renderCapacityTab(cap)}
        {activeTab === 'sources' && renderSourcesTab(overview, sourcesDetail)}
        {activeTab === 'rules' && renderRulesTab(rulesList)}
      </main>
    </div>
  );
}

function renderOverviewTab(
  overview: OverviewResponse,
  cap: any,
  incidents: Incident[],
  openIncidentDetail: (inc: Incident) => void,
  setActiveTab: (t: TabKey) => void
) {
  return (
    <div>
      {/* KPI Summary Cards */}
      <div className="kpi-grid">
        <div className="kpi-card">
          <div className="kpi-label">Total Reported GPU Capacity</div>
          <div className="kpi-value">
            {cap.total_allocatable_gpus} / {cap.total_capacity_gpus}{' '}
            <span style={{ fontSize: '13px', fontWeight: 500, color: 'var(--text-secondary)' }}>
              Allocatable
            </span>
          </div>
          <div className="kpi-sub">
            {cap.unavailable_hardware_gpus > 0
              ? `${cap.unavailable_hardware_gpus} GPU(s) de-allocated by device plugin`
              : '100% of physical GPUs allocatable'}
          </div>
        </div>

        <div className="kpi-card">
          <div className="kpi-label">Allocated vs Free GPUs</div>
          <div className="kpi-value">
            {cap.total_allocated_gpus} Allocated · {cap.total_free_allocatable_gpus} Free
          </div>
          <div className="kpi-sub">
            Pending GPU Requests: <strong>{cap.pending_requested_gpus} GPUs</strong>
          </div>
        </div>

        <div className="kpi-card">
          <div className="kpi-label">Active Diagnostic Findings</div>
          <div className="kpi-value">{incidents.length}</div>
          <div className="kpi-sub">
            {overview.active_incident_counts?.critical || 0} critical,{' '}
            {overview.active_incident_counts?.warning || 0} warning,{' '}
            {overview.active_incident_counts?.inconclusive || 0} inconclusive
          </div>
        </div>

        <div className="kpi-card">
          <div className="kpi-label">Cluster Mean GPU Utilization</div>
          <div className="kpi-value">
            {cap.cluster_mean_gpu_util_percent !== undefined && cap.cluster_mean_gpu_util_percent !== null
              ? `${cap.cluster_mean_gpu_util_percent}%`
              : 'N/A (Stale)'}
          </div>
          <div className="kpi-sub">
            Mean VRAM Used:{' '}
            {cap.cluster_mean_fb_used_percent !== undefined && cap.cluster_mean_fb_used_percent !== null
              ? `${cap.cluster_mean_fb_used_percent}%`
              : 'N/A'}
          </div>
        </div>

        <div className="kpi-card">
          <div className="kpi-label">Low-Util GPU-Hours (&lt;15%)</div>
          <div className="kpi-value">{cap.observed_low_util_gpu_hours} GPU-h</div>
          <div className="kpi-sub">
            Out of {cap.total_observed_allocated_gpu_hours} mapped GPU-h observed
          </div>
        </div>

        <div className="kpi-card">
          <div className="kpi-label">Telemetry &amp; Pod Attribution</div>
          <div className="kpi-value">
            {cap.telemetry_fresh_gpus} Fresh / {cap.telemetry_stale_gpus} Stale
          </div>
          <div className="kpi-sub">
            {cap.unmapped_allocated_gpus > 0
              ? `${cap.unmapped_allocated_gpus} allocated GPU(s) missing pod labels`
              : 'All allocated GPUs mapped to pods'}
          </div>
        </div>
      </div>

      {cap.attribution_blind_spot_warning && (
        <div className="blindspot-box">
          <strong>Observability Blind Spot Warning:</strong> {cap.attribution_blind_spot_warning}
        </div>
      )}

      <div className="section-grid-2">
        {/* Active Incidents Table */}
        <div className="panel">
          <div className="panel-header">
            <div className="panel-title">Active Diagnostic Incidents ({incidents.length})</div>
            <button className="btn" onClick={() => setActiveTab('incidents')}>
              View All / Filter
            </button>
          </div>
          {incidents.length === 0 ? (
            <div className="empty-state">
              <strong>Zero Active Incidents</strong>
              <p style={{ marginTop: '4px' }}>
                All 6 diagnostic rules evaluated against fresh cluster telemetry with zero threshold violations.
              </p>
            </div>
          ) : (
            <table className="data-table">
              <thead>
                <tr>
                  <th>Severity</th>
                  <th>Rule</th>
                  <th>Finding &amp; Summary</th>
                  <th>Evidence Strength</th>
                  <th>Action</th>
                </tr>
              </thead>
              <tbody>
                {incidents.map((inc) => (
                  <tr
                    key={inc.id}
                    className="incident-row"
                    onClick={() => openIncidentDetail(inc)}
                  >
                    <td>
                      <span className={`badge ${inc.severity}`}>{inc.severity}</span>
                      {inc.eval_status === 'inconclusive' && (
                        <div style={{ marginTop: '4px' }}>
                          <span className="badge inconclusive">inconclusive</span>
                        </div>
                      )}
                    </td>
                    <td className="mono">{inc.rule_id}</td>
                    <td>
                      <div style={{ fontWeight: 700, color: '#f8fafc' }}>{inc.title}</div>
                      <div style={{ color: 'var(--text-secondary)', fontSize: '12px', marginTop: '2px' }}>
                        {inc.summary}
                      </div>
                    </td>
                    <td>
                      <span className={`badge ${inc.evidence_strength}`}>
                        {inc.evidence_strength}
                      </span>
                    </td>
                    <td>
                      <button
                        className="btn btn-primary"
                        onClick={(e: Event) => {
                          e.stopPropagation();
                          openIncidentDetail(inc);
                        }}
                      >
                        Inspect
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>

        {/* Node & Source Health Summary */}
        <div>
          <div className="panel">
            <div className="panel-header">
              <div className="panel-title">Node GPU Capacity &amp; Health Summary</div>
              <button className="btn" onClick={() => setActiveTab('capacity')}>
                Capacity Report
              </button>
            </div>
            <table className="data-table">
              <thead>
                <tr>
                  <th>Node</th>
                  <th>Cap / Alloc / Used</th>
                  <th>Mean Util</th>
                  <th>Status</th>
                </tr>
              </thead>
              <tbody>
                {(cap.nodes || []).map((n: any) => (
                  <tr key={n.node_name}>
                    <td>
                      <div className="mono" style={{ fontWeight: 700 }}>{n.node_name}</div>
                      <div style={{ fontSize: '11.5px', color: 'var(--text-secondary)' }}>{n.gpu_model}</div>
                    </td>
                    <td className="mono">
                      {n.capacity_gpus} / {n.allocatable_gpus} / {n.allocated_gpus}
                      {n.unavailable_gpus > 0 && (
                        <span style={{ color: '#fca5a5', display: 'block' }}>
                          (-{n.unavailable_gpus} unavail)
                        </span>
                      )}
                    </td>
                    <td className="mono">
                      {n.mean_gpu_util_percent !== undefined && n.mean_gpu_util_percent !== null
                        ? `${n.mean_gpu_util_percent}%`
                        : 'Stale'}
                    </td>
                    <td>
                      <span
                        className={`badge ${
                          n.status_note.includes('Healthy') ? 'healthy' : 'warning'
                        }`}
                      >
                        {n.status_note}
                      </span>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>

          <div className="panel">
            <div className="panel-header">
              <div className="panel-title">Pending GPU Workloads ({(cap.pending_workloads || []).length})</div>
            </div>
            {(cap.pending_workloads || []).length === 0 ? (
              <div style={{ color: 'var(--text-secondary)', fontSize: '13px' }}>
                No pending GPU workloads in queue.
              </div>
            ) : (
              <table className="data-table">
                <thead>
                  <tr>
                    <th>Workload Pod</th>
                    <th>Req GPUs</th>
                    <th>Wait</th>
                    <th>Observed Scheduler Reason</th>
                  </tr>
                </thead>
                <tbody>
                  {(cap.pending_workloads || []).map((pw: any) => (
                    <tr key={`${pw.namespace}/${pw.pod_name}`}>
                      <td className="mono">
                        {pw.namespace}/{pw.pod_name}
                      </td>
                      <td className="mono">{pw.requested_gpus}</td>
                      <td className="mono">{pw.pending_minutes}m</td>
                      <td style={{ fontSize: '12px', color: '#fcd34d' }}>
                        <strong>{pw.observed_reason}:</strong> {pw.scheduler_message}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}

function renderIncidentsTab(
  incidents: Incident[],
  severityFilter: string,
  setSeverityFilter: (v: string) => void,
  categoryFilter: string,
  setCategoryFilter: (v: string) => void,
  searchQuery: string,
  setSearchQuery: (v: string) => void,
  openIncidentDetail: (inc: Incident) => void
) {
  return (
    <div className="panel">
      <div className="panel-header">
        <div className="panel-title">Searchable &amp; Filterable Incident Queue</div>
        <span className="mono" style={{ color: 'var(--text-secondary)' }}>
          Showing {incidents.length} incident(s)
        </span>
      </div>

      <div className="filter-bar">
        <input
          type="text"
          className="filter-input"
          placeholder="Search by pod, GPU UUID, node, XID, or rule ID..."
          value={searchQuery}
          onInput={(e: Event) => setSearchQuery((e.target as HTMLInputElement).value)}
        />
        <select
          className="filter-select"
          value={severityFilter}
          onChange={(e: Event) => setSeverityFilter((e.target as HTMLSelectElement).value)}
        >
          <option value="all">All Severities</option>
          <option value="critical">Critical Only</option>
          <option value="warning">Warning Only</option>
          <option value="info">Info Only</option>
        </select>
        <select
          className="filter-select"
          value={categoryFilter}
          onChange={(e: Event) => setCategoryFilter((e.target as HTMLSelectElement).value)}
        >
          <option value="all">All Categories</option>
          <option value="scheduling">Scheduling (GPU-SCHED-001)</option>
          <option value="hardware_health">Hardware Health (GPU-HEALTH-002)</option>
          <option value="utilization">Low Utilization (GPU-UTIL-003)</option>
          <option value="failure_correlation">Failure Correlation (GPU-CORR-004)</option>
          <option value="data_freshness">Data Freshness (GPU-FRESH-005)</option>
          <option value="thermal_power_memory">Thermal / Power / Memory (GPU-THERM-006)</option>
        </select>
      </div>

      {incidents.length === 0 ? (
        <div className="empty-state">
          <strong>No Incidents Match Current Filters</strong>
          <p style={{ marginTop: '4px' }}>
            Try clearing the search filter or selecting another scenario in the top banner.
          </p>
        </div>
      ) : (
        <table className="data-table">
          <thead>
            <tr>
              <th>Incident ID / Fingerprint</th>
              <th>Severity &amp; Status</th>
              <th>Rule</th>
              <th>Title &amp; Summary</th>
              <th>Evidence Strength</th>
              <th>Occurrences</th>
              <th>Inspect</th>
            </tr>
          </thead>
          <tbody>
            {incidents.map((inc) => (
              <tr key={inc.id} className="incident-row" onClick={() => openIncidentDetail(inc)}>
                <td className="mono">
                  <div>{inc.id}</div>
                  <div style={{ color: 'var(--text-muted)', fontSize: '11px' }}>
                    fp:{inc.fingerprint}
                  </div>
                </td>
                <td>
                  <span className={`badge ${inc.severity}`}>{inc.severity}</span>
                  <div style={{ marginTop: '4px' }}>
                    <span className={`badge ${inc.eval_status}`}>{inc.eval_status}</span>
                  </div>
                </td>
                <td className="mono">
                  <div>{inc.rule_id}</div>
                  <div style={{ fontSize: '11px', color: 'var(--text-secondary)' }}>
                    {inc.category}
                  </div>
                </td>
                <td>
                  <div style={{ fontWeight: 700 }}>{inc.title}</div>
                  <div style={{ color: 'var(--text-secondary)', fontSize: '12px', marginTop: '3px' }}>
                    {inc.summary}
                  </div>
                  <div style={{ marginTop: '5px', display: 'flex', gap: '6px', flexWrap: 'wrap' }}>
                    {(inc.affected_resources || []).map((r) => (
                      <span
                        key={r}
                        className="mono"
                        style={{
                          background: 'var(--bg-elevated)',
                          padding: '1px 6px',
                          borderRadius: '4px',
                          fontSize: '11px',
                        }}
                      >
                        {r}
                      </span>
                    ))}
                  </div>
                </td>
                <td>
                  <span className={`badge ${inc.evidence_strength}`}>{inc.evidence_strength}</span>
                </td>
                <td className="mono">{inc.occurrence_count}x</td>
                <td>
                  <button
                    className="btn btn-primary"
                    onClick={(e: Event) => {
                      e.stopPropagation();
                      openIncidentDetail(inc);
                    }}
                  >
                    Open Evidence
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}

function renderIncidentDetailTab(
  selected: Incident | null,
  allIncidents: Incident[],
  onSelect: (inc: Incident) => void
) {
  if (!selected) {
    return (
      <div className="panel">
        <div className="empty-state">
          <strong>No Active Incident Selected</strong>
          <p style={{ marginTop: '6px' }}>
            The currently selected scenario has zero active incidents, or none is selected. Switch to Scenarios 2–6 to inspect detailed multi-signal incident reports.
          </p>
        </div>
      </div>
    );
  }

  return (
    <div>
      {allIncidents.length > 1 && (
        <div className="panel" style={{ padding: '10px 16px' }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: '8px', flexWrap: 'wrap' }}>
            <span style={{ fontSize: '12px', color: 'var(--text-secondary)', fontWeight: 600 }}>
              Switch Incident in Scenario ({allIncidents.length}):
            </span>
            {allIncidents.map((inc) => (
              <button
                key={inc.id}
                className={`btn ${inc.id === selected.id ? 'btn-primary' : ''}`}
                onClick={() => onSelect(inc)}
              >
                [{inc.rule_id}] {inc.title.slice(0, 48)}...
              </button>
            ))}
          </div>
        </div>
      )}

      <div className="panel">
        <div className="panel-header">
          <div>
            <div style={{ display: 'flex', alignItems: 'center', gap: '8px', marginBottom: '6px' }}>
              <span className={`badge ${selected.severity}`}>{selected.severity}</span>
              <span className={`badge ${selected.eval_status}`}>{selected.eval_status}</span>
              <span className={`badge ${selected.evidence_strength}`}>
                Evidence: {selected.evidence_strength}
              </span>
              <span className="mono" style={{ color: 'var(--text-secondary)' }}>
                Rule: {selected.rule_id} ({selected.rule_name})
              </span>
            </div>
            <h2 style={{ fontSize: '18px', fontWeight: 700 }}>{selected.title}</h2>
          </div>
          <div className="mono" style={{ textAlign: 'right', fontSize: '12px', color: 'var(--text-secondary)' }}>
            <div>ID: {selected.id}</div>
            <div>Fingerprint: {selected.fingerprint}</div>
            <div>Occurrences: {selected.occurrence_count}</div>
          </div>
        </div>

        <p style={{ fontSize: '14px', color: '#e2e8f0', marginBottom: '12px' }}>{selected.summary}</p>

        <div className="disclaimer-box">
          <strong>Evidence Strength Basis ({selected.evidence_strength.toUpperCase()}):</strong>{' '}
          {selected.evidence_strength_basis}
          {selected.inconclusive_reason && (
            <div style={{ marginTop: '4px', color: '#e9d5ff' }}>
              <strong>Inconclusive Safeguard:</strong> {selected.inconclusive_reason}
            </div>
          )}
        </div>
      </div>

      <div className="section-grid-2">
        {/* Observed Facts vs Possible Causes */}
        <div className="panel">
          <div className="panel-header">
            <div className="panel-title">Observed Facts (Verified Signals)</div>
            <span className="badge healthy">Directly Measured</span>
          </div>
          <ul className="bullet-list">
            {(selected.observed_facts || []).map((f, i) => (
              <li key={i}>{f}</li>
            ))}
          </ul>

          <div className="panel-header" style={{ marginTop: '18px' }}>
            <div className="panel-title">Possible Explanations (Hypotheses to Verify — Not Proven Causation)</div>
            <span className="badge info">Hypotheses</span>
          </div>
          <ul className="bullet-list">
            {(selected.possible_causes || []).map((c, i) => (
              <li key={i}>{c}</li>
            ))}
          </ul>
        </div>

        {/* Supporting vs Contradicting Evidence */}
        <div className="panel">
          <div className="panel-header">
            <div className="panel-title">
              Supporting Evidence ({(selected.supporting_evidence || []).length})
            </div>
          </div>
          {(selected.supporting_evidence || []).map((ev, i) => (
            <div key={i} className="evidence-box supporting">
              <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: '12px' }}>
                <span className="mono" style={{ color: '#fca5a5', fontWeight: 700 }}>
                  [{ev.kind}] {ev.object_ref} {ev.metric_name ? `· ${ev.metric_name}` : ''}
                </span>
                <span className="mono" style={{ color: 'var(--text-secondary)' }}>
                  {ev.observed_at}
                </span>
              </div>
              <div style={{ marginTop: '4px', fontWeight: 600 }}>{ev.summary}</div>
              <div className="mono" style={{ marginTop: '3px', fontSize: '11.5px', color: 'var(--text-secondary)' }}>
                Observed Value: {ev.value} {ev.threshold ? `| Rule Threshold: ${ev.threshold}` : ''}
              </div>
            </div>
          ))}

          <div className="panel-header" style={{ marginTop: '16px' }}>
            <div className="panel-title">
              Contradicting &amp; Contextual Evidence ({(selected.contradicting_evidence || []).length})
            </div>
          </div>
          {(selected.contradicting_evidence || []).length === 0 ? (
            <div style={{ color: 'var(--text-secondary)', fontSize: '12.5px' }}>
              No contradicting signals observed for this incident.
            </div>
          ) : (
            (selected.contradicting_evidence || []).map((ev, i) => (
              <div key={i} className={`evidence-box ${ev.polarity}`}>
                <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: '12px' }}>
                  <span className="mono" style={{ color: '#6ee7b7', fontWeight: 700 }}>
                    [{ev.polarity.toUpperCase()}] {ev.object_ref}{' '}
                    {ev.metric_name ? `· ${ev.metric_name}` : ''}
                  </span>
                  <span className="mono" style={{ color: 'var(--text-secondary)' }}>
                    {ev.observed_at}
                  </span>
                </div>
                <div style={{ marginTop: '4px', fontWeight: 600 }}>{ev.summary}</div>
                <div className="mono" style={{ marginTop: '3px', fontSize: '11.5px', color: 'var(--text-secondary)' }}>
                  Value: {ev.value}
                </div>
              </div>
            ))
          )}
        </div>
      </div>

      <div className="section-grid-2">
        {/* Correlated Timeline */}
        <div className="panel">
          <div className="panel-header">
            <div className="panel-title">Correlated Multi-Signal Timeline</div>
          </div>
          {(selected.timeline || []).length === 0 ? (
            <div style={{ color: 'var(--text-secondary)' }}>No discrete timeline transitions recorded.</div>
          ) : (
            <div className="timeline-list">
              {(selected.timeline || []).map((t, i) => (
                <div key={i} className="timeline-item">
                  <span className="mono" style={{ color: 'var(--text-secondary)' }}>
                    {t.timestamp}
                  </span>
                  <span className={`badge ${t.severity}`}>{t.layer}</span>
                  <div>
                    <strong className="mono">{t.event_type}</strong> ({t.object_ref}): {t.summary}
                  </div>
                </div>
              ))}
            </div>
          )}
        </div>

        {/* Suggested Operator Next Checks & Affected Resources */}
        <div className="panel">
          <div className="panel-header">
            <div className="panel-title">Recommended Engineering Next Checks</div>
          </div>
          <ul className="bullet-list">
            {(selected.suggested_checks || []).map((chk, i) => (
              <li key={i}>{chk}</li>
            ))}
          </ul>

          <div className="panel-header" style={{ marginTop: '18px' }}>
            <div className="panel-title">Affected Infrastructure Resources</div>
          </div>
          <div style={{ display: 'flex', gap: '8px', flexWrap: 'wrap' }}>
            {(selected.affected_resources || []).map((res) => (
              <span
                key={res}
                className="mono"
                style={{
                  background: 'var(--bg-elevated)',
                  border: '1px solid var(--border-strong)',
                  padding: '4px 10px',
                  borderRadius: '5px',
                }}
              >
                {res}
              </span>
            ))}
          </div>
        </div>
      </div>
    </div>
  );
}

function renderCapacityTab(cap: any) {
  const history: UtilizationHistoryPoint[] = cap.historical_utilization || [];
  const meth = cap.methodology || {};

  return (
    <div>
      {cap.attribution_blind_spot_warning && (
        <div className="blindspot-box">
          <strong>Telemetry Attribution Blind Spot:</strong> {cap.attribution_blind_spot_warning}
        </div>
      )}

      <div className="kpi-grid">
        <div className="kpi-card">
          <div className="kpi-label">Physical / Allocatable GPUs</div>
          <div className="kpi-value">
            {cap.total_capacity_gpus} / {cap.total_allocatable_gpus}
          </div>
          <div className="kpi-sub">
            Unavailable Hardware GPUs: {cap.unavailable_hardware_gpus}
          </div>
        </div>
        <div className="kpi-card">
          <div className="kpi-label">Allocated / Free Allocatable</div>
          <div className="kpi-value">
            {cap.total_allocated_gpus} / {cap.total_free_allocatable_gpus}
          </div>
          <div className="kpi-sub">Pending Queue Requests: {cap.pending_requested_gpus} GPUs</div>
        </div>
        <div className="kpi-card">
          <div className="kpi-label">Observed Allocated GPU-Hours</div>
          <div className="kpi-value">{cap.total_observed_allocated_gpu_hours} GPU-h</div>
          <div className="kpi-sub">
            Below {meth.util_threshold_percent}% SM threshold: <strong>{cap.observed_low_util_gpu_hours} GPU-h</strong>
          </div>
        </div>
        <div className="kpi-card">
          <div className="kpi-label">Illustrative Sample Cost Est.</div>
          <div className="kpi-value">
            {cap.optional_low_util_sample_cost_usd !== undefined && cap.optional_low_util_sample_cost_usd !== null
              ? `$${cap.optional_low_util_sample_cost_usd.toFixed(2)}`
              : 'Disabled'}
          </div>
          <div className="kpi-sub">
            Sample rate: ${meth.optional_sample_hourly_rate_usd?.toFixed(2) || '0.00'}/GPU-h (Estimate only — NOT realized savings)
          </div>
        </div>
      </div>

      {/* Historical GPU Utilization Chart (only rendered when sufficient samples exist) */}
      <div className="panel">
        <div className="panel-header">
          <div className="panel-title">
            Historical Cluster GPU Utilization vs. Framebuffer VRAM Residency ({history.length} time-series buckets)
          </div>
          <span className="mono" style={{ fontSize: '12px', color: 'var(--text-secondary)' }}>
            Blue = Mean SM Compute Util (%) · Emerald = Mean Framebuffer VRAM Used (%)
          </span>
        </div>
        {history.length < 3 ? (
          <div className="empty-state">
            <strong>Insufficient Historical Samples for Chart Rendering</strong>
            <p style={{ marginTop: '4px' }}>
              GPU Lens renders historical charts only when at least 3 fresh time-series buckets exist.
            </p>
          </div>
        ) : (
          renderUtilizationSVGChart(history, meth.util_threshold_percent || 15)
        )}
      </div>

      {/* Persistently Low-Utilization Allocation Windows */}
      <div className="panel">
        <div className="panel-header">
          <div className="panel-title">
            Persistently Low-Utilization Allocation Windows ({(cap.low_utilization_workloads || []).length})
          </div>
          <span className="badge info">Threshold &lt; {meth.util_threshold_percent}% over &gt;= {meth.min_window_minutes}m</span>
        </div>
        {(cap.low_utilization_workloads || []).length === 0 ? (
          <div className="empty-state">
            <strong>No Persistently Low-Utilization Workloads Observed</strong>
            <p style={{ marginTop: '4px' }}>
              All mapped running workloads either exceed the {meth.util_threshold_percent}% SM compute threshold or lack complete pod attribution labels.
            </p>
          </div>
        ) : (
          <div>
            <table className="data-table">
              <thead>
                <tr>
                  <th>Workload Pod &amp; Owner</th>
                  <th>Node &amp; GPUs</th>
                  <th>Window &amp; Samples</th>
                  <th>SM Compute Util</th>
                  <th>Mem Copy &amp; VRAM Used</th>
                  <th>Low-Util GPU-Hours</th>
                  <th>Workload Context &amp; Follow-Up Checklist</th>
                </tr>
              </thead>
              <tbody>
                {(cap.low_utilization_workloads || []).map((w: any) => (
                  <tr key={`${w.namespace}/${w.pod_name}`}>
                    <td>
                      <div className="mono" style={{ fontWeight: 700 }}>
                        {w.namespace}/{w.pod_name}
                      </div>
                      <div style={{ fontSize: '11.5px', color: 'var(--text-secondary)' }}>
                        {w.owner_ref}
                      </div>
                    </td>
                    <td className="mono">
                      <div>{w.node_name}</div>
                      <div style={{ fontSize: '11px', color: 'var(--text-secondary)' }}>
                        {w.allocated_gpus} GPU(s): {(w.assigned_gpu_uuids || []).join(', ')}
                      </div>
                    </td>
                    <td className="mono">
                      <div>{w.observation_window_minutes} min</div>
                      <div style={{ fontSize: '11px', color: 'var(--text-secondary)' }}>
                        {w.sample_count} samples (last: {w.last_data_update})
                      </div>
                    </td>
                    <td className="mono">
                      <strong style={{ color: '#fcd34d' }}>{w.mean_gpu_util_percent}% mean</strong>
                      <div style={{ fontSize: '11px', color: 'var(--text-secondary)' }}>
                        min {w.min_gpu_util_percent}% / max {w.max_gpu_util_percent}%
                      </div>
                    </td>
                    <td className="mono">
                      <div>VRAM: {w.mean_fb_used_mib} MiB ({w.mean_fb_used_percent}%)</div>
                      <div style={{ fontSize: '11px', color: 'var(--text-secondary)' }}>
                        MemCopy: {w.mean_mem_copy_util_percent}%
                      </div>
                    </td>
                    <td className="mono">
                      <div>{w.low_util_gpu_hours} GPU-h</div>
                      {w.optional_sample_cost_estimate_usd !== undefined && (
                        <div style={{ fontSize: '11px', color: 'var(--text-secondary)' }}>
                          Sample est: ${w.optional_sample_cost_estimate_usd.toFixed(2)}
                        </div>
                      )}
                    </td>
                    <td style={{ maxWidth: '380px' }}>
                      <div style={{ fontWeight: 600, fontSize: '12px', color: '#fde68a', marginBottom: '4px' }}>
                        {w.workload_context_note}
                      </div>
                      <ul className="bullet-list" style={{ fontSize: '11.5px', color: 'var(--text-secondary)' }}>
                        {(w.follow_up_checklist || []).map((item: string, idx: number) => (
                          <li key={idx}>{item}</li>
                        ))}
                      </ul>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}

        <div className="disclaimer-box">
          <strong>Non-Waste Engineering Policy:</strong> {meth.non_waste_disclaimer}
        </div>
      </div>

      {/* Transparent Calculation Methodology */}
      <div className="panel">
        <div className="panel-header">
          <div className="panel-title">Transparent Capacity &amp; Low-Utilization Calculation Methodology</div>
        </div>
        <p className="mono" style={{ marginBottom: '10px', color: '#7dd3fc' }}>
          {meth.formula_summary}
        </p>
        <ul className="bullet-list" style={{ marginBottom: '10px' }}>
          {(meth.step_by_step_calculation || []).map((s: string, i: number) => (
            <li key={i}>{s}</li>
          ))}
        </ul>
        <p style={{ fontSize: '12.5px', color: 'var(--text-secondary)' }}>
          <strong>Unmapped Telemetry Policy:</strong> {meth.missing_mapping_policy}
        </p>
        {meth.optional_cost_estimate_label && (
          <p style={{ fontSize: '12.5px', color: '#fcd34d', marginTop: '6px' }}>
            <strong>Sample Rate Estimate Notice:</strong> {meth.optional_cost_estimate_label}
          </p>
        )}
      </div>
    </div>
  );
}

function renderUtilizationSVGChart(points: UtilizationHistoryPoint[], threshold: number) {
  const width = 920;
  const height = 210;
  const padL = 48;
  const padR = 24;
  const padT = 18;
  const padB = 34;
  const plotW = width - padL - padR;
  const plotH = height - padT - padB;

  const xFor = (idx: number) =>
    points.length <= 1 ? padL : padL + (idx / (points.length - 1)) * plotW;
  const yFor = (pct: number) => padT + plotH - (Math.max(0, Math.min(100, pct)) / 100) * plotH;

  const utilPath = points
    .map((p, i) => `${i === 0 ? 'M' : 'L'} ${xFor(i).toFixed(1)} ${yFor(p.mean_gpu_util_percent).toFixed(1)}`)
    .join(' ');
  const fbPath = points
    .map((p, i) => `${i === 0 ? 'M' : 'L'} ${xFor(i).toFixed(1)} ${yFor(p.mean_fb_used_percent).toFixed(1)}`)
    .join(' ');
  const threshY = yFor(threshold);

  return (
    <div style={{ overflowX: 'auto' }}>
      <svg viewBox={`0 0 ${width} ${height}`} style={{ width: '100%', maxHeight: '230px' }}>
        {[0, 25, 50, 75, 100].map((v) => {
          const y = yFor(v);
          return (
            <g key={v}>
              <line x1={padL} y1={y} x2={width - padR} y2={y} stroke="#1e293b" strokeWidth="1" />
              <text x={padL - 8} y={y + 4} textAnchor="end" fill="#64748b" fontSize="10">
                {v}%
              </text>
            </g>
          );
        })}
        {/* Low-util threshold reference line */}
        <line
          x1={padL}
          y1={threshY}
          x2={width - padR}
          y2={threshY}
          stroke="#f59e0b"
          strokeDasharray="4 4"
          strokeWidth="1.2"
        />
        <text x={width - padR - 4} y={threshY - 5} textAnchor="end" fill="#f59e0b" fontSize="10">
          Low-Util Review Threshold ({threshold}%)
        </text>

        <path d={fbPath} fill="none" stroke="#10b981" strokeWidth="2.2" />
        <path d={utilPath} fill="none" stroke="#38bdf8" strokeWidth="2.5" />

        {points.map((p, i) => (
          <g key={i}>
            <circle cx={xFor(i)} cy={yFor(p.mean_gpu_util_percent)} r="3.2" fill="#38bdf8" />
            <circle cx={xFor(i)} cy={yFor(p.mean_fb_used_percent)} r="2.8" fill="#10b981" />
            {(i === 0 || i === points.length - 1 || i % 2 === 0) && (
              <text
                x={xFor(i)}
                y={height - 10}
                textAnchor="middle"
                fill="#94a3b8"
                fontSize="10"
              >
                {p.timestamp.slice(11, 16)}Z
              </text>
            )}
          </g>
        ))}
      </svg>
    </div>
  );
}

function renderSourcesTab(overview: OverviewResponse, sourcesDetail: any) {
  const sources: any[] = overview.sources || [];
  const mappings = sourcesDetail?.metric_mappings || {};
  const exported = sourcesDetail?.exported_metrics || {};

  return (
    <div>
      <div className="panel">
        <div className="panel-header">
          <div className="panel-title">Configured Telemetry &amp; Cluster State Sources</div>
          <span className="badge healthy">Read-Only Transport Enforced</span>
        </div>
        <table className="data-table">
          <thead>
            <tr>
              <th>Source Name &amp; ID</th>
              <th>Kind / Mode</th>
              <th>Endpoint</th>
              <th>Availability &amp; Freshness</th>
              <th>Exported Metrics</th>
              <th>Notes &amp; Errors</th>
            </tr>
          </thead>
          <tbody>
            {sources.map((s) => (
              <tr key={s.id}>
                <td>
                  <div style={{ fontWeight: 700 }}>{s.name}</div>
                  <div className="mono" style={{ color: 'var(--text-secondary)', fontSize: '11.5px' }}>
                    {s.id}
                  </div>
                </td>
                <td>
                  <span className="badge info">{s.kind}</span>{' '}
                  <span className="badge contextual">{s.mode}</span>
                </td>
                <td className="mono">{s.endpoint}</td>
                <td>
                  <span className={`badge ${s.available && !s.is_stale ? 'healthy' : 'critical'}`}>
                    {s.available && !s.is_stale ? 'Available & Fresh' : 'Stale / Unavailable'}
                  </span>
                  <div className="mono" style={{ fontSize: '11.5px', marginTop: '4px', color: 'var(--text-secondary)' }}>
                    Last success: {s.last_success_at} ({s.staleness_seconds}s ago)
                  </div>
                </td>
                <td className="mono">
                  <div>Required: {s.exported_required_count}</div>
                  <div>Optional: {s.exported_optional_count}</div>
                  {(s.missing_optional || []).length > 0 && (
                    <div style={{ fontSize: '11px', color: 'var(--text-secondary)' }}>
                      Unexported optional: {(s.missing_optional || []).join(', ')}
                    </div>
                  )}
                </td>
                <td style={{ maxWidth: '340px', fontSize: '12px' }}>
                  {s.last_error && (
                    <div className="mono" style={{ color: '#fca5a5', marginBottom: '4px' }}>
                      Error: {s.last_error}
                    </div>
                  )}
                  <div style={{ color: 'var(--text-secondary)' }}>{s.notes}</div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <div className="panel">
        <div className="panel-header">
          <div className="panel-title">Configurable DCGM Exporter Metric &amp; Label Mappings</div>
        </div>
        <div className="section-grid-2">
          <div>
            <h4 style={{ marginBottom: '8px', color: '#7dd3fc' }}>Active Metric Field Mappings</h4>
            <table className="data-table">
              <thead>
                <tr>
                  <th>Canonical Signal</th>
                  <th>Configured Prometheus / DCGM Name</th>
                  <th>Exported in Snapshot</th>
                </tr>
              </thead>
              <tbody>
                {Object.entries(mappings)
                  .filter(([k]) => k.endsWith('_metric'))
                  .map(([k, v]) => {
                    const metricName = String(v);
                    const isPresent = Boolean(exported[metricName]);
                    return (
                      <tr key={k}>
                        <td className="mono">{k}</td>
                        <td className="mono">{metricName}</td>
                        <td>
                          <span className={`badge ${isPresent ? 'healthy' : 'info'}`}>
                            {isPresent ? 'Exported' : 'Optional / Not Exported'}
                          </span>
                        </td>
                      </tr>
                    );
                  })}
              </tbody>
            </table>
          </div>
          <div>
            <h4 style={{ marginBottom: '8px', color: '#7dd3fc' }}>Label Resolution Priority</h4>
            <p style={{ fontSize: '12.5px', color: 'var(--text-secondary)', marginBottom: '10px' }}>
              Supports both standard <span className="mono">dcgm-exporter --kubernetes</span> labels and legacy{' '}
              <span className="mono">--use-old-namespace</span> conventions:
            </p>
            <ul className="bullet-list mono" style={{ fontSize: '12px' }}>
              <li>GPU UUID Labels: {(mappings.uuid_label_candidates || []).join(', ')}</li>
              <li>Node Hostname Labels: {(mappings.node_name_label_candidates || []).join(', ')}</li>
              <li>Pod Name Labels: {(mappings.pod_name_label_candidates || []).join(', ')}</li>
              <li>Pod Namespace Labels: {(mappings.namespace_label_candidates || []).join(', ')}</li>
              <li>Container Name Labels: {(mappings.container_label_candidates || []).join(', ')}</li>
            </ul>
          </div>
        </div>
      </div>
    </div>
  );
}

function renderRulesTab(rulesList: RuleDefinition[]) {
  return (
    <div>
      {rulesList.map((r) => (
        <div key={r.id} className="panel">
          <div className="panel-header">
            <div style={{ display: 'flex', alignItems: 'center', gap: '10px' }}>
              <span className="mono" style={{ fontWeight: 800, fontSize: '15px', color: '#7dd3fc' }}>
                {r.id}
              </span>
              <span className="panel-title">{r.name}</span>
              <span className={`badge ${r.default_severity}`}>Default: {r.default_severity}</span>
              <span className="badge info">{r.category}</span>
            </div>
          </div>
          <p style={{ marginBottom: '12px', color: '#e2e8f0' }}>{r.description}</p>

          <div className="section-grid-2">
            <div>
              <h4 style={{ fontSize: '12.5px', color: '#93c5fd', marginBottom: '6px' }}>
                Configurable Thresholds &amp; Observation Windows
              </h4>
              <table className="data-table">
                <thead>
                  <tr>
                    <th>Parameter</th>
                    <th>Active Value</th>
                    <th>Purpose</th>
                  </tr>
                </thead>
                <tbody>
                  {(r.thresholds || []).map((t) => (
                    <tr key={t.key}>
                      <td className="mono">{t.key}</td>
                      <td className="mono" style={{ fontWeight: 700 }}>
                        {t.value} {t.unit}
                      </td>
                      <td style={{ fontSize: '12px', color: 'var(--text-secondary)' }}>
                        {t.description}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>

            <div>
              <h4 style={{ fontSize: '12.5px', color: '#93c5fd', marginBottom: '6px' }}>
                Required &amp; Optional Input Signals
              </h4>
              <div style={{ marginBottom: '8px', fontSize: '12.5px' }}>
                <strong>Required:</strong>{' '}
                <span className="mono">{(r.required_signals || []).join(', ')}</span>
              </div>
              {(r.optional_signals || []).length > 0 && (
                <div style={{ marginBottom: '8px', fontSize: '12.5px' }}>
                  <strong>Optional (used when exported):</strong>{' '}
                  <span className="mono">{(r.optional_signals || []).join(', ')}</span>
                </div>
              )}

              <h4 style={{ fontSize: '12.5px', color: '#fcd34d', marginTop: '10px', marginBottom: '4px' }}>
                Suppression &amp; Inconclusive Safeguards
              </h4>
              <ul className="bullet-list" style={{ fontSize: '12px', color: 'var(--text-secondary)' }}>
                {(r.suppression_conditions || []).map((c, idx) => (
                  <li key={`sup-${idx}`}>
                    <strong>Suppressed when:</strong> {c}
                  </li>
                ))}
                {(r.inconclusive_conditions || []).map((c, idx) => (
                  <li key={`inc-${idx}`}>
                    <strong>Inconclusive when:</strong> {c}
                  </li>
                ))}
              </ul>
            </div>
          </div>
        </div>
      ))}
    </div>
  );
}
