(function() {
  const __modules = {};
  const __cache = {};
  function __resolve(fromId, request) {
    if (request.charAt(0) !== '.') throw new Error("Unsupported bare import: " + request + " (from " + fromId + ")");
    const parts = fromId.split('/');
    parts.pop();
    for (const seg of request.split('/')) {
      if (seg === '.' || seg === '') continue;
      if (seg === '..') { parts.pop(); continue; }
      parts.push(seg);
    }
    return parts.join('/');
  }
  function __require(id) {
    if (__cache[id]) return __cache[id].exports;
    const fn = __modules[id];
    if (!fn) throw new Error("Module not found: " + id);
    const mod = { exports: {} };
    __cache[id] = mod;
    fn(mod, mod.exports, function(request) { return __require(__resolve(id, request)); });
    return mod.exports;
  }
  __modules["api"] = function(module, exports, require) {
"use strict";
Object.defineProperty(exports, "__esModule", { value: true });
exports.fetchScenarios = fetchScenarios;
exports.selectScenario = selectScenario;
exports.fetchOverview = fetchOverview;
exports.fetchIncidents = fetchIncidents;
exports.fetchIncidentDetail = fetchIncidentDetail;
exports.fetchCapacity = fetchCapacity;
exports.fetchSources = fetchSources;
exports.fetchRules = fetchRules;
async function getJSON(url) {
    const resp = await fetch(url, {
        headers: { Accept: 'application/json' },
    });
    if (!resp.ok) {
        const text = await resp.text();
        throw new Error(`HTTP ${resp.status}: ${text}`);
    }
    return resp.json();
}
async function fetchScenarios() {
    return getJSON('/api/v1/scenarios');
}
async function selectScenario(scenarioId) {
    const resp = await fetch('/api/v1/scenarios/select', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ scenario_id: scenarioId }),
    });
    if (!resp.ok) {
        const text = await resp.text();
        throw new Error(`HTTP ${resp.status}: ${text}`);
    }
    return resp.json();
}
async function fetchOverview(scenarioId) {
    const q = scenarioId ? `?scenario=${encodeURIComponent(scenarioId)}` : '';
    return getJSON(`/api/v1/overview${q}`);
}
async function fetchIncidents(params) {
    const sp = new URLSearchParams();
    if (params.severity && params.severity !== 'all')
        sp.set('severity', params.severity);
    if (params.category && params.category !== 'all')
        sp.set('category', params.category);
    if (params.q)
        sp.set('q', params.q);
    const qs = sp.toString();
    return getJSON(`/api/v1/incidents${qs ? '?' + qs : ''}`);
}
async function fetchIncidentDetail(id) {
    return getJSON(`/api/v1/incidents/${encodeURIComponent(id)}`);
}
async function fetchCapacity() {
    return getJSON('/api/v1/capacity');
}
async function fetchSources() {
    return getJSON('/api/v1/sources');
}
async function fetchRules() {
    return getJSON('/api/v1/rules');
}

  };
  __modules["App"] = function(module, exports, require) {
"use strict";
Object.defineProperty(exports, "__esModule", { value: true });
exports.App = App;
exports.renderIncidentsTab = renderIncidentsTab;
exports.renderIncidentDetailTab = renderIncidentDetailTab;
exports.renderCapacityTab = renderCapacityTab;
exports.renderSourcesTab = renderSourcesTab;
exports.renderRulesTab = renderRulesTab;
const jsx_runtime_1 = require("./jsx-runtime");
const api_1 = require("./api");
function getInitialQueryParam(name, fallback) {
    try {
        const sp = new URLSearchParams(window.location.search);
        return sp.get(name) || fallback;
    }
    catch {
        return fallback;
    }
}
function App() {
    const initialTab = getInitialQueryParam('tab', 'overview');
    const initialScenarioParam = getInitialQueryParam('scenario', '');
    const [activeTab, setActiveTab] = (0, jsx_runtime_1.useState)(initialTab);
    const [scenarios, setScenarios] = (0, jsx_runtime_1.useState)([]);
    const [selectedScenarioId, setSelectedScenarioId] = (0, jsx_runtime_1.useState)(initialScenarioParam);
    const [overview, setOverview] = (0, jsx_runtime_1.useState)(null);
    const [rulesList, setRulesList] = (0, jsx_runtime_1.useState)([]);
    const [sourcesDetail, setSourcesDetail] = (0, jsx_runtime_1.useState)(null);
    const [selectedIncident, setSelectedIncident] = (0, jsx_runtime_1.useState)(null);
    const [loading, setLoading] = (0, jsx_runtime_1.useState)(true);
    const [errorMsg, setErrorMsg] = (0, jsx_runtime_1.useState)('');
    // Incident list filters
    const [severityFilter, setSeverityFilter] = (0, jsx_runtime_1.useState)('all');
    const [categoryFilter, setCategoryFilter] = (0, jsx_runtime_1.useState)('all');
    const [searchQuery, setSearchQuery] = (0, jsx_runtime_1.useState)('');
    (0, jsx_runtime_1.useEffect)(() => {
        loadAllData(initialScenarioParam);
    }, []);
    async function loadAllData(targetScenario) {
        setLoading(true);
        setErrorMsg('');
        try {
            let ov;
            if (targetScenario) {
                ov = await (0, api_1.selectScenario)(targetScenario);
            }
            else {
                ov = await (0, api_1.fetchOverview)();
            }
            const [scenResp, rulesResp, srcResp] = await Promise.all([
                (0, api_1.fetchScenarios)(),
                (0, api_1.fetchRules)(),
                (0, api_1.fetchSources)(),
            ]);
            setOverview(ov);
            setScenarios(scenResp.scenarios || []);
            setSelectedScenarioId(ov.scenario_id);
            setRulesList(rulesResp.rules || []);
            setSourcesDetail(srcResp);
            if (ov.active_incidents && ov.active_incidents.length > 0) {
                setSelectedIncident(ov.active_incidents[0]);
            }
            else {
                setSelectedIncident(null);
            }
        }
        catch (err) {
            setErrorMsg(err?.message || 'Failed to load cluster diagnostics from backend API.');
        }
        finally {
            setLoading(false);
        }
    }
    async function handleScenarioChange(ev) {
        const target = ev.target;
        const nextId = target.value;
        setSelectedScenarioId(nextId);
        await loadAllData(nextId);
    }
    function openIncidentDetail(inc) {
        setSelectedIncident(inc);
        setActiveTab('incident_detail');
    }
    if (loading && !overview) {
        return ((0, jsx_runtime_1.h)("div", { className: "main-container" },
            (0, jsx_runtime_1.h)("div", { className: "panel" },
                (0, jsx_runtime_1.h)("div", { className: "panel-title" }, "Loading GPU Lens Diagnostics & Telemetry..."),
                (0, jsx_runtime_1.h)("p", { style: { color: 'var(--text-secondary)', marginTop: '8px' } }, "Evaluating normalized cluster snapshot against deterministic rule registry."))));
    }
    if (errorMsg) {
        return ((0, jsx_runtime_1.h)("div", { className: "main-container" },
            (0, jsx_runtime_1.h)("div", { className: "panel", style: { borderColor: 'var(--status-critical)' } },
                (0, jsx_runtime_1.h)("div", { className: "panel-title", style: { color: '#fca5a5' } }, "Backend Diagnostic API Error"),
                (0, jsx_runtime_1.h)("p", { style: { marginTop: '8px' }, className: "mono" }, errorMsg),
                (0, jsx_runtime_1.h)("button", { className: "btn btn-primary", style: { marginTop: '12px' }, onClick: () => loadAllData() }, "Retry Collection"))));
    }
    if (!overview) {
        return (0, jsx_runtime_1.h)("div", { className: "main-container" }, "No cluster snapshot available.");
    }
    const cap = overview.capacity_summary;
    const incidents = overview.active_incidents || [];
    const filteredIncidents = incidents.filter((inc) => {
        if (severityFilter !== 'all' && inc.severity !== severityFilter)
            return false;
        if (categoryFilter !== 'all' && inc.category !== categoryFilter)
            return false;
        if (searchQuery.trim() !== '') {
            const q = searchQuery.toLowerCase();
            const matchTitle = inc.title.toLowerCase().includes(q);
            const matchSummary = inc.summary.toLowerCase().includes(q);
            const matchRule = inc.rule_id.toLowerCase().includes(q);
            const matchRes = (inc.affected_resources || []).some((r) => r.toLowerCase().includes(q));
            if (!matchTitle && !matchSummary && !matchRule && !matchRes)
                return false;
        }
        return true;
    });
    const isSimulated = overview.data_mode === 'simulated_fixture';
    return ((0, jsx_runtime_1.h)("div", null,
        (0, jsx_runtime_1.h)("div", { className: "sim-banner" },
            (0, jsx_runtime_1.h)("div", { style: { display: 'flex', alignItems: 'center', gap: '12px', flexWrap: 'wrap' } },
                (0, jsx_runtime_1.h)("span", { className: "sim-badge" }, isSimulated ? 'SIMULATED OFFLINE FIXTURE DATA' : 'LIVE INFRASTRUCTURE TELEMETRY'),
                (0, jsx_runtime_1.h)("span", { style: { color: '#cbd5e1' } }, isSimulated
                    ? 'Deterministic offline demonstration — zero AI APIs, zero GPU hardware required. All findings are produced by explicit rule evaluation.'
                    : 'Connected to read-only Kubernetes & Prometheus endpoints.')),
            (0, jsx_runtime_1.h)("div", { className: "scenario-bar" },
                (0, jsx_runtime_1.h)("label", { style: { color: '#93c5fd', fontWeight: 600, fontSize: '12px' } }, "Active Scenario:"),
                (0, jsx_runtime_1.h)("select", { className: "scenario-select", value: selectedScenarioId, onChange: handleScenarioChange }, scenarios.map((s) => ((0, jsx_runtime_1.h)("option", { key: s.id, value: s.id, selected: s.id === selectedScenarioId }, s.name)))))),
        (0, jsx_runtime_1.h)("header", { className: "top-header" },
            (0, jsx_runtime_1.h)("div", { className: "brand-group" },
                (0, jsx_runtime_1.h)("div", { className: "brand-logo" }, "GL"),
                (0, jsx_runtime_1.h)("div", null,
                    (0, jsx_runtime_1.h)("div", { className: "brand-title" }, "GPU Lens"),
                    (0, jsx_runtime_1.h)("div", { className: "brand-subtitle" },
                        "GPU Fleet Reliability & Capacity Intelligence \u00B7 Cluster:",
                        ' ',
                        (0, jsx_runtime_1.h)("span", { className: "mono", style: { color: '#e2e8f0' } }, overview.cluster_name)))),
            (0, jsx_runtime_1.h)("nav", { className: "nav-tabs" },
                (0, jsx_runtime_1.h)("button", { className: `nav-tab ${activeTab === 'overview' ? 'active' : ''}`, onClick: () => setActiveTab('overview') }, "Overview"),
                (0, jsx_runtime_1.h)("button", { className: `nav-tab ${activeTab === 'incidents' ? 'active' : ''}`, onClick: () => setActiveTab('incidents') },
                    "Incidents ",
                    (0, jsx_runtime_1.h)("span", { className: "tab-count" }, incidents.length)),
                (0, jsx_runtime_1.h)("button", { className: `nav-tab ${activeTab === 'incident_detail' ? 'active' : ''}`, onClick: () => setActiveTab('incident_detail') }, "Incident Detail"),
                (0, jsx_runtime_1.h)("button", { className: `nav-tab ${activeTab === 'capacity' ? 'active' : ''}`, onClick: () => setActiveTab('capacity') }, "Capacity & Utilization"),
                (0, jsx_runtime_1.h)("button", { className: `nav-tab ${activeTab === 'sources' ? 'active' : ''}`, onClick: () => setActiveTab('sources') },
                    "Data Sources ",
                    (0, jsx_runtime_1.h)("span", { className: "tab-count" }, (overview.sources || []).length)),
                (0, jsx_runtime_1.h)("button", { className: `nav-tab ${activeTab === 'rules' ? 'active' : ''}`, onClick: () => setActiveTab('rules') },
                    "Rules Registry ",
                    (0, jsx_runtime_1.h)("span", { className: "tab-count" }, rulesList.length)))),
        (0, jsx_runtime_1.h)("main", { className: "main-container" },
            (0, jsx_runtime_1.h)("div", { className: "scenario-context-card" },
                (0, jsx_runtime_1.h)("div", null,
                    (0, jsx_runtime_1.h)("div", { style: { fontWeight: 700, fontSize: '14px', color: '#f8fafc' } }, overview.scenario_name),
                    (0, jsx_runtime_1.h)("div", { style: { color: 'var(--text-secondary)', fontSize: '13px', marginTop: '2px' } }, overview.scenario_description)),
                (0, jsx_runtime_1.h)("div", { style: { textAlign: 'right', minWidth: '210px' } },
                    (0, jsx_runtime_1.h)("div", { className: "mono", style: { fontSize: '11.5px', color: 'var(--text-secondary)' } },
                        "Snapshot Time: ",
                        overview.collected_at),
                    (0, jsx_runtime_1.h)("div", { style: { marginTop: '4px' } },
                        (0, jsx_runtime_1.h)("span", { className: "badge healthy" }, "Read-Only Enforced")))),
            (0, jsx_runtime_1.h)("div", { className: `health-banner ${overview.fleet_health_status}` },
                (0, jsx_runtime_1.h)("div", null,
                    (0, jsx_runtime_1.h)("div", { style: { display: 'flex', alignItems: 'center', gap: '10px' } },
                        (0, jsx_runtime_1.h)("span", { className: `badge ${overview.fleet_health_status}` }, overview.fleet_health_status.replace(/_/g, ' ')),
                        (0, jsx_runtime_1.h)("strong", { style: { fontSize: '14.5px' } }, overview.fleet_health_headline))),
                (0, jsx_runtime_1.h)("div", { className: "mono", style: { fontSize: '12px' } },
                    "Critical: ",
                    overview.active_incident_counts?.critical || 0,
                    " \u00B7 Warning:",
                    ' ',
                    overview.active_incident_counts?.warning || 0,
                    " \u00B7 Inconclusive:",
                    ' ',
                    overview.active_incident_counts?.inconclusive || 0)),
            activeTab === 'overview' && renderOverviewTab(overview, cap, incidents, openIncidentDetail, setActiveTab),
            activeTab === 'incidents' &&
                renderIncidentsTab(filteredIncidents, severityFilter, setSeverityFilter, categoryFilter, setCategoryFilter, searchQuery, setSearchQuery, openIncidentDetail),
            activeTab === 'incident_detail' &&
                renderIncidentDetailTab(selectedIncident, incidents, openIncidentDetail),
            activeTab === 'capacity' && renderCapacityTab(cap),
            activeTab === 'sources' && renderSourcesTab(overview, sourcesDetail),
            activeTab === 'rules' && renderRulesTab(rulesList))));
}
function renderOverviewTab(overview, cap, incidents, openIncidentDetail, setActiveTab) {
    return ((0, jsx_runtime_1.h)("div", null,
        (0, jsx_runtime_1.h)("div", { className: "kpi-grid" },
            (0, jsx_runtime_1.h)("div", { className: "kpi-card" },
                (0, jsx_runtime_1.h)("div", { className: "kpi-label" }, "Total Reported GPU Capacity"),
                (0, jsx_runtime_1.h)("div", { className: "kpi-value" },
                    cap.total_allocatable_gpus,
                    " / ",
                    cap.total_capacity_gpus,
                    ' ',
                    (0, jsx_runtime_1.h)("span", { style: { fontSize: '13px', fontWeight: 500, color: 'var(--text-secondary)' } }, "Allocatable")),
                (0, jsx_runtime_1.h)("div", { className: "kpi-sub" }, cap.unavailable_hardware_gpus > 0
                    ? `${cap.unavailable_hardware_gpus} GPU(s) de-allocated by device plugin`
                    : '100% of physical GPUs allocatable')),
            (0, jsx_runtime_1.h)("div", { className: "kpi-card" },
                (0, jsx_runtime_1.h)("div", { className: "kpi-label" }, "Allocated vs Free GPUs"),
                (0, jsx_runtime_1.h)("div", { className: "kpi-value" },
                    cap.total_allocated_gpus,
                    " Allocated \u00B7 ",
                    cap.total_free_allocatable_gpus,
                    " Free"),
                (0, jsx_runtime_1.h)("div", { className: "kpi-sub" },
                    "Pending GPU Requests: ",
                    (0, jsx_runtime_1.h)("strong", null,
                        cap.pending_requested_gpus,
                        " GPUs"))),
            (0, jsx_runtime_1.h)("div", { className: "kpi-card" },
                (0, jsx_runtime_1.h)("div", { className: "kpi-label" }, "Active Diagnostic Findings"),
                (0, jsx_runtime_1.h)("div", { className: "kpi-value" }, incidents.length),
                (0, jsx_runtime_1.h)("div", { className: "kpi-sub" },
                    overview.active_incident_counts?.critical || 0,
                    " critical,",
                    ' ',
                    overview.active_incident_counts?.warning || 0,
                    " warning,",
                    ' ',
                    overview.active_incident_counts?.inconclusive || 0,
                    " inconclusive")),
            (0, jsx_runtime_1.h)("div", { className: "kpi-card" },
                (0, jsx_runtime_1.h)("div", { className: "kpi-label" }, "Cluster Mean GPU Utilization"),
                (0, jsx_runtime_1.h)("div", { className: "kpi-value" }, cap.cluster_mean_gpu_util_percent !== undefined && cap.cluster_mean_gpu_util_percent !== null
                    ? `${cap.cluster_mean_gpu_util_percent}%`
                    : 'N/A (Stale)'),
                (0, jsx_runtime_1.h)("div", { className: "kpi-sub" },
                    "Mean VRAM Used:",
                    ' ',
                    cap.cluster_mean_fb_used_percent !== undefined && cap.cluster_mean_fb_used_percent !== null
                        ? `${cap.cluster_mean_fb_used_percent}%`
                        : 'N/A')),
            (0, jsx_runtime_1.h)("div", { className: "kpi-card" },
                (0, jsx_runtime_1.h)("div", { className: "kpi-label" }, "Low-Util GPU-Hours (<15%)"),
                (0, jsx_runtime_1.h)("div", { className: "kpi-value" },
                    cap.observed_low_util_gpu_hours,
                    " GPU-h"),
                (0, jsx_runtime_1.h)("div", { className: "kpi-sub" },
                    "Out of ",
                    cap.total_observed_allocated_gpu_hours,
                    " mapped GPU-h observed")),
            (0, jsx_runtime_1.h)("div", { className: "kpi-card" },
                (0, jsx_runtime_1.h)("div", { className: "kpi-label" }, "Telemetry & Pod Attribution"),
                (0, jsx_runtime_1.h)("div", { className: "kpi-value" },
                    cap.telemetry_fresh_gpus,
                    " Fresh / ",
                    cap.telemetry_stale_gpus,
                    " Stale"),
                (0, jsx_runtime_1.h)("div", { className: "kpi-sub" }, cap.unmapped_allocated_gpus > 0
                    ? `${cap.unmapped_allocated_gpus} allocated GPU(s) missing pod labels`
                    : 'All allocated GPUs mapped to pods'))),
        cap.attribution_blind_spot_warning && ((0, jsx_runtime_1.h)("div", { className: "blindspot-box" },
            (0, jsx_runtime_1.h)("strong", null, "Observability Blind Spot Warning:"),
            " ",
            cap.attribution_blind_spot_warning)),
        (0, jsx_runtime_1.h)("div", { className: "section-grid-2" },
            (0, jsx_runtime_1.h)("div", { className: "panel" },
                (0, jsx_runtime_1.h)("div", { className: "panel-header" },
                    (0, jsx_runtime_1.h)("div", { className: "panel-title" },
                        "Active Diagnostic Incidents (",
                        incidents.length,
                        ")"),
                    (0, jsx_runtime_1.h)("button", { className: "btn", onClick: () => setActiveTab('incidents') }, "View All / Filter")),
                incidents.length === 0 ? ((0, jsx_runtime_1.h)("div", { className: "empty-state" },
                    (0, jsx_runtime_1.h)("strong", null, "Zero Active Incidents"),
                    (0, jsx_runtime_1.h)("p", { style: { marginTop: '4px' } }, "All 6 diagnostic rules evaluated against fresh cluster telemetry with zero threshold violations."))) : ((0, jsx_runtime_1.h)("table", { className: "data-table" },
                    (0, jsx_runtime_1.h)("thead", null,
                        (0, jsx_runtime_1.h)("tr", null,
                            (0, jsx_runtime_1.h)("th", null, "Severity"),
                            (0, jsx_runtime_1.h)("th", null, "Rule"),
                            (0, jsx_runtime_1.h)("th", null, "Finding & Summary"),
                            (0, jsx_runtime_1.h)("th", null, "Evidence Strength"),
                            (0, jsx_runtime_1.h)("th", null, "Action"))),
                    (0, jsx_runtime_1.h)("tbody", null, incidents.map((inc) => ((0, jsx_runtime_1.h)("tr", { key: inc.id, className: "incident-row", onClick: () => openIncidentDetail(inc) },
                        (0, jsx_runtime_1.h)("td", null,
                            (0, jsx_runtime_1.h)("span", { className: `badge ${inc.severity}` }, inc.severity),
                            inc.eval_status === 'inconclusive' && ((0, jsx_runtime_1.h)("div", { style: { marginTop: '4px' } },
                                (0, jsx_runtime_1.h)("span", { className: "badge inconclusive" }, "inconclusive")))),
                        (0, jsx_runtime_1.h)("td", { className: "mono" }, inc.rule_id),
                        (0, jsx_runtime_1.h)("td", null,
                            (0, jsx_runtime_1.h)("div", { style: { fontWeight: 700, color: '#f8fafc' } }, inc.title),
                            (0, jsx_runtime_1.h)("div", { style: { color: 'var(--text-secondary)', fontSize: '12px', marginTop: '2px' } }, inc.summary)),
                        (0, jsx_runtime_1.h)("td", null,
                            (0, jsx_runtime_1.h)("span", { className: `badge ${inc.evidence_strength}` }, inc.evidence_strength)),
                        (0, jsx_runtime_1.h)("td", null,
                            (0, jsx_runtime_1.h)("button", { className: "btn btn-primary", onClick: (e) => {
                                    e.stopPropagation();
                                    openIncidentDetail(inc);
                                } }, "Inspect"))))))))),
            (0, jsx_runtime_1.h)("div", null,
                (0, jsx_runtime_1.h)("div", { className: "panel" },
                    (0, jsx_runtime_1.h)("div", { className: "panel-header" },
                        (0, jsx_runtime_1.h)("div", { className: "panel-title" }, "Node GPU Capacity & Health Summary"),
                        (0, jsx_runtime_1.h)("button", { className: "btn", onClick: () => setActiveTab('capacity') }, "Capacity Report")),
                    (0, jsx_runtime_1.h)("table", { className: "data-table" },
                        (0, jsx_runtime_1.h)("thead", null,
                            (0, jsx_runtime_1.h)("tr", null,
                                (0, jsx_runtime_1.h)("th", null, "Node"),
                                (0, jsx_runtime_1.h)("th", null, "Cap / Alloc / Used"),
                                (0, jsx_runtime_1.h)("th", null, "Mean Util"),
                                (0, jsx_runtime_1.h)("th", null, "Status"))),
                        (0, jsx_runtime_1.h)("tbody", null, (cap.nodes || []).map((n) => ((0, jsx_runtime_1.h)("tr", { key: n.node_name },
                            (0, jsx_runtime_1.h)("td", null,
                                (0, jsx_runtime_1.h)("div", { className: "mono", style: { fontWeight: 700 } }, n.node_name),
                                (0, jsx_runtime_1.h)("div", { style: { fontSize: '11.5px', color: 'var(--text-secondary)' } }, n.gpu_model)),
                            (0, jsx_runtime_1.h)("td", { className: "mono" },
                                n.capacity_gpus,
                                " / ",
                                n.allocatable_gpus,
                                " / ",
                                n.allocated_gpus,
                                n.unavailable_gpus > 0 && ((0, jsx_runtime_1.h)("span", { style: { color: '#fca5a5', display: 'block' } },
                                    "(-",
                                    n.unavailable_gpus,
                                    " unavail)"))),
                            (0, jsx_runtime_1.h)("td", { className: "mono" }, n.mean_gpu_util_percent !== undefined && n.mean_gpu_util_percent !== null
                                ? `${n.mean_gpu_util_percent}%`
                                : 'Stale'),
                            (0, jsx_runtime_1.h)("td", null,
                                (0, jsx_runtime_1.h)("span", { className: `badge ${n.status_note.includes('Healthy') ? 'healthy' : 'warning'}` }, n.status_note)))))))),
                (0, jsx_runtime_1.h)("div", { className: "panel" },
                    (0, jsx_runtime_1.h)("div", { className: "panel-header" },
                        (0, jsx_runtime_1.h)("div", { className: "panel-title" },
                            "Pending GPU Workloads (",
                            (cap.pending_workloads || []).length,
                            ")")),
                    (cap.pending_workloads || []).length === 0 ? ((0, jsx_runtime_1.h)("div", { style: { color: 'var(--text-secondary)', fontSize: '13px' } }, "No pending GPU workloads in queue.")) : ((0, jsx_runtime_1.h)("table", { className: "data-table" },
                        (0, jsx_runtime_1.h)("thead", null,
                            (0, jsx_runtime_1.h)("tr", null,
                                (0, jsx_runtime_1.h)("th", null, "Workload Pod"),
                                (0, jsx_runtime_1.h)("th", null, "Req GPUs"),
                                (0, jsx_runtime_1.h)("th", null, "Wait"),
                                (0, jsx_runtime_1.h)("th", null, "Observed Scheduler Reason"))),
                        (0, jsx_runtime_1.h)("tbody", null, (cap.pending_workloads || []).map((pw) => ((0, jsx_runtime_1.h)("tr", { key: `${pw.namespace}/${pw.pod_name}` },
                            (0, jsx_runtime_1.h)("td", { className: "mono" },
                                pw.namespace,
                                "/",
                                pw.pod_name),
                            (0, jsx_runtime_1.h)("td", { className: "mono" }, pw.requested_gpus),
                            (0, jsx_runtime_1.h)("td", { className: "mono" },
                                pw.pending_minutes,
                                "m"),
                            (0, jsx_runtime_1.h)("td", { style: { fontSize: '12px', color: '#fcd34d' } },
                                (0, jsx_runtime_1.h)("strong", null,
                                    pw.observed_reason,
                                    ":"),
                                " ",
                                pw.scheduler_message))))))))))));
}
function renderIncidentsTab(incidents, severityFilter, setSeverityFilter, categoryFilter, setCategoryFilter, searchQuery, setSearchQuery, openIncidentDetail) {
    return ((0, jsx_runtime_1.h)("div", { className: "panel" },
        (0, jsx_runtime_1.h)("div", { className: "panel-header" },
            (0, jsx_runtime_1.h)("div", { className: "panel-title" }, "Searchable & Filterable Incident Queue"),
            (0, jsx_runtime_1.h)("span", { className: "mono", style: { color: 'var(--text-secondary)' } },
                "Showing ",
                incidents.length,
                " incident(s)")),
        (0, jsx_runtime_1.h)("div", { className: "filter-bar" },
            (0, jsx_runtime_1.h)("input", { type: "text", className: "filter-input", placeholder: "Search by pod, GPU UUID, node, XID, or rule ID...", value: searchQuery, onInput: (e) => setSearchQuery(e.target.value) }),
            (0, jsx_runtime_1.h)("select", { className: "filter-select", value: severityFilter, onChange: (e) => setSeverityFilter(e.target.value) },
                (0, jsx_runtime_1.h)("option", { value: "all" }, "All Severities"),
                (0, jsx_runtime_1.h)("option", { value: "critical" }, "Critical Only"),
                (0, jsx_runtime_1.h)("option", { value: "warning" }, "Warning Only"),
                (0, jsx_runtime_1.h)("option", { value: "info" }, "Info Only")),
            (0, jsx_runtime_1.h)("select", { className: "filter-select", value: categoryFilter, onChange: (e) => setCategoryFilter(e.target.value) },
                (0, jsx_runtime_1.h)("option", { value: "all" }, "All Categories"),
                (0, jsx_runtime_1.h)("option", { value: "scheduling" }, "Scheduling (GPU-SCHED-001)"),
                (0, jsx_runtime_1.h)("option", { value: "hardware_health" }, "Hardware Health (GPU-HEALTH-002)"),
                (0, jsx_runtime_1.h)("option", { value: "utilization" }, "Low Utilization (GPU-UTIL-003)"),
                (0, jsx_runtime_1.h)("option", { value: "failure_correlation" }, "Failure Correlation (GPU-CORR-004)"),
                (0, jsx_runtime_1.h)("option", { value: "data_freshness" }, "Data Freshness (GPU-FRESH-005)"),
                (0, jsx_runtime_1.h)("option", { value: "thermal_power_memory" }, "Thermal / Power / Memory (GPU-THERM-006)"))),
        incidents.length === 0 ? ((0, jsx_runtime_1.h)("div", { className: "empty-state" },
            (0, jsx_runtime_1.h)("strong", null, "No Incidents Match Current Filters"),
            (0, jsx_runtime_1.h)("p", { style: { marginTop: '4px' } }, "Try clearing the search filter or selecting another scenario in the top banner."))) : ((0, jsx_runtime_1.h)("table", { className: "data-table" },
            (0, jsx_runtime_1.h)("thead", null,
                (0, jsx_runtime_1.h)("tr", null,
                    (0, jsx_runtime_1.h)("th", null, "Incident ID / Fingerprint"),
                    (0, jsx_runtime_1.h)("th", null, "Severity & Status"),
                    (0, jsx_runtime_1.h)("th", null, "Rule"),
                    (0, jsx_runtime_1.h)("th", null, "Title & Summary"),
                    (0, jsx_runtime_1.h)("th", null, "Evidence Strength"),
                    (0, jsx_runtime_1.h)("th", null, "Occurrences"),
                    (0, jsx_runtime_1.h)("th", null, "Inspect"))),
            (0, jsx_runtime_1.h)("tbody", null, incidents.map((inc) => ((0, jsx_runtime_1.h)("tr", { key: inc.id, className: "incident-row", onClick: () => openIncidentDetail(inc) },
                (0, jsx_runtime_1.h)("td", { className: "mono" },
                    (0, jsx_runtime_1.h)("div", null, inc.id),
                    (0, jsx_runtime_1.h)("div", { style: { color: 'var(--text-muted)', fontSize: '11px' } },
                        "fp:",
                        inc.fingerprint)),
                (0, jsx_runtime_1.h)("td", null,
                    (0, jsx_runtime_1.h)("span", { className: `badge ${inc.severity}` }, inc.severity),
                    (0, jsx_runtime_1.h)("div", { style: { marginTop: '4px' } },
                        (0, jsx_runtime_1.h)("span", { className: `badge ${inc.eval_status}` }, inc.eval_status))),
                (0, jsx_runtime_1.h)("td", { className: "mono" },
                    (0, jsx_runtime_1.h)("div", null, inc.rule_id),
                    (0, jsx_runtime_1.h)("div", { style: { fontSize: '11px', color: 'var(--text-secondary)' } }, inc.category)),
                (0, jsx_runtime_1.h)("td", null,
                    (0, jsx_runtime_1.h)("div", { style: { fontWeight: 700 } }, inc.title),
                    (0, jsx_runtime_1.h)("div", { style: { color: 'var(--text-secondary)', fontSize: '12px', marginTop: '3px' } }, inc.summary),
                    (0, jsx_runtime_1.h)("div", { style: { marginTop: '5px', display: 'flex', gap: '6px', flexWrap: 'wrap' } }, (inc.affected_resources || []).map((r) => ((0, jsx_runtime_1.h)("span", { key: r, className: "mono", style: {
                            background: 'var(--bg-elevated)',
                            padding: '1px 6px',
                            borderRadius: '4px',
                            fontSize: '11px',
                        } }, r))))),
                (0, jsx_runtime_1.h)("td", null,
                    (0, jsx_runtime_1.h)("span", { className: `badge ${inc.evidence_strength}` }, inc.evidence_strength)),
                (0, jsx_runtime_1.h)("td", { className: "mono" },
                    inc.occurrence_count,
                    "x"),
                (0, jsx_runtime_1.h)("td", null,
                    (0, jsx_runtime_1.h)("button", { className: "btn btn-primary", onClick: (e) => {
                            e.stopPropagation();
                            openIncidentDetail(inc);
                        } }, "Open Evidence"))))))))));
}
function renderIncidentDetailTab(selected, allIncidents, onSelect) {
    if (!selected) {
        return ((0, jsx_runtime_1.h)("div", { className: "panel" },
            (0, jsx_runtime_1.h)("div", { className: "empty-state" },
                (0, jsx_runtime_1.h)("strong", null, "No Active Incident Selected"),
                (0, jsx_runtime_1.h)("p", { style: { marginTop: '6px' } }, "The currently selected scenario has zero active incidents, or none is selected. Switch to Scenarios 2\u20136 to inspect detailed multi-signal incident reports."))));
    }
    return ((0, jsx_runtime_1.h)("div", null,
        allIncidents.length > 1 && ((0, jsx_runtime_1.h)("div", { className: "panel", style: { padding: '10px 16px' } },
            (0, jsx_runtime_1.h)("div", { style: { display: 'flex', alignItems: 'center', gap: '8px', flexWrap: 'wrap' } },
                (0, jsx_runtime_1.h)("span", { style: { fontSize: '12px', color: 'var(--text-secondary)', fontWeight: 600 } },
                    "Switch Incident in Scenario (",
                    allIncidents.length,
                    "):"),
                allIncidents.map((inc) => ((0, jsx_runtime_1.h)("button", { key: inc.id, className: `btn ${inc.id === selected.id ? 'btn-primary' : ''}`, onClick: () => onSelect(inc) },
                    "[",
                    inc.rule_id,
                    "] ",
                    inc.title.slice(0, 48),
                    "...")))))),
        (0, jsx_runtime_1.h)("div", { className: "panel" },
            (0, jsx_runtime_1.h)("div", { className: "panel-header" },
                (0, jsx_runtime_1.h)("div", null,
                    (0, jsx_runtime_1.h)("div", { style: { display: 'flex', alignItems: 'center', gap: '8px', marginBottom: '6px' } },
                        (0, jsx_runtime_1.h)("span", { className: `badge ${selected.severity}` }, selected.severity),
                        (0, jsx_runtime_1.h)("span", { className: `badge ${selected.eval_status}` }, selected.eval_status),
                        (0, jsx_runtime_1.h)("span", { className: `badge ${selected.evidence_strength}` },
                            "Evidence: ",
                            selected.evidence_strength),
                        (0, jsx_runtime_1.h)("span", { className: "mono", style: { color: 'var(--text-secondary)' } },
                            "Rule: ",
                            selected.rule_id,
                            " (",
                            selected.rule_name,
                            ")")),
                    (0, jsx_runtime_1.h)("h2", { style: { fontSize: '18px', fontWeight: 700 } }, selected.title)),
                (0, jsx_runtime_1.h)("div", { className: "mono", style: { textAlign: 'right', fontSize: '12px', color: 'var(--text-secondary)' } },
                    (0, jsx_runtime_1.h)("div", null,
                        "ID: ",
                        selected.id),
                    (0, jsx_runtime_1.h)("div", null,
                        "Fingerprint: ",
                        selected.fingerprint),
                    (0, jsx_runtime_1.h)("div", null,
                        "Occurrences: ",
                        selected.occurrence_count))),
            (0, jsx_runtime_1.h)("p", { style: { fontSize: '14px', color: '#e2e8f0', marginBottom: '12px' } }, selected.summary),
            (0, jsx_runtime_1.h)("div", { className: "disclaimer-box" },
                (0, jsx_runtime_1.h)("strong", null,
                    "Evidence Strength Basis (",
                    selected.evidence_strength.toUpperCase(),
                    "):"),
                ' ',
                selected.evidence_strength_basis,
                selected.inconclusive_reason && ((0, jsx_runtime_1.h)("div", { style: { marginTop: '4px', color: '#e9d5ff' } },
                    (0, jsx_runtime_1.h)("strong", null, "Inconclusive Safeguard:"),
                    " ",
                    selected.inconclusive_reason)))),
        (0, jsx_runtime_1.h)("div", { className: "section-grid-2" },
            (0, jsx_runtime_1.h)("div", { className: "panel" },
                (0, jsx_runtime_1.h)("div", { className: "panel-header" },
                    (0, jsx_runtime_1.h)("div", { className: "panel-title" }, "Observed Facts (Verified Signals)"),
                    (0, jsx_runtime_1.h)("span", { className: "badge healthy" }, "Directly Measured")),
                (0, jsx_runtime_1.h)("ul", { className: "bullet-list" }, (selected.observed_facts || []).map((f, i) => ((0, jsx_runtime_1.h)("li", { key: i }, f)))),
                (0, jsx_runtime_1.h)("div", { className: "panel-header", style: { marginTop: '18px' } },
                    (0, jsx_runtime_1.h)("div", { className: "panel-title" }, "Possible Explanations (Hypotheses to Verify \u2014 Not Proven Causation)"),
                    (0, jsx_runtime_1.h)("span", { className: "badge info" }, "Hypotheses")),
                (0, jsx_runtime_1.h)("ul", { className: "bullet-list" }, (selected.possible_causes || []).map((c, i) => ((0, jsx_runtime_1.h)("li", { key: i }, c))))),
            (0, jsx_runtime_1.h)("div", { className: "panel" },
                (0, jsx_runtime_1.h)("div", { className: "panel-header" },
                    (0, jsx_runtime_1.h)("div", { className: "panel-title" },
                        "Supporting Evidence (",
                        (selected.supporting_evidence || []).length,
                        ")")),
                (selected.supporting_evidence || []).map((ev, i) => ((0, jsx_runtime_1.h)("div", { key: i, className: "evidence-box supporting" },
                    (0, jsx_runtime_1.h)("div", { style: { display: 'flex', justifyContent: 'space-between', fontSize: '12px' } },
                        (0, jsx_runtime_1.h)("span", { className: "mono", style: { color: '#fca5a5', fontWeight: 700 } },
                            "[",
                            ev.kind,
                            "] ",
                            ev.object_ref,
                            " ",
                            ev.metric_name ? `· ${ev.metric_name}` : ''),
                        (0, jsx_runtime_1.h)("span", { className: "mono", style: { color: 'var(--text-secondary)' } }, ev.observed_at)),
                    (0, jsx_runtime_1.h)("div", { style: { marginTop: '4px', fontWeight: 600 } }, ev.summary),
                    (0, jsx_runtime_1.h)("div", { className: "mono", style: { marginTop: '3px', fontSize: '11.5px', color: 'var(--text-secondary)' } },
                        "Observed Value: ",
                        ev.value,
                        " ",
                        ev.threshold ? `| Rule Threshold: ${ev.threshold}` : '')))),
                (0, jsx_runtime_1.h)("div", { className: "panel-header", style: { marginTop: '16px' } },
                    (0, jsx_runtime_1.h)("div", { className: "panel-title" },
                        "Contradicting & Contextual Evidence (",
                        (selected.contradicting_evidence || []).length,
                        ")")),
                (selected.contradicting_evidence || []).length === 0 ? ((0, jsx_runtime_1.h)("div", { style: { color: 'var(--text-secondary)', fontSize: '12.5px' } }, "No contradicting signals observed for this incident.")) : ((selected.contradicting_evidence || []).map((ev, i) => ((0, jsx_runtime_1.h)("div", { key: i, className: `evidence-box ${ev.polarity}` },
                    (0, jsx_runtime_1.h)("div", { style: { display: 'flex', justifyContent: 'space-between', fontSize: '12px' } },
                        (0, jsx_runtime_1.h)("span", { className: "mono", style: { color: '#6ee7b7', fontWeight: 700 } },
                            "[",
                            ev.polarity.toUpperCase(),
                            "] ",
                            ev.object_ref,
                            ' ',
                            ev.metric_name ? `· ${ev.metric_name}` : ''),
                        (0, jsx_runtime_1.h)("span", { className: "mono", style: { color: 'var(--text-secondary)' } }, ev.observed_at)),
                    (0, jsx_runtime_1.h)("div", { style: { marginTop: '4px', fontWeight: 600 } }, ev.summary),
                    (0, jsx_runtime_1.h)("div", { className: "mono", style: { marginTop: '3px', fontSize: '11.5px', color: 'var(--text-secondary)' } },
                        "Value: ",
                        ev.value))))))),
        (0, jsx_runtime_1.h)("div", { className: "section-grid-2" },
            (0, jsx_runtime_1.h)("div", { className: "panel" },
                (0, jsx_runtime_1.h)("div", { className: "panel-header" },
                    (0, jsx_runtime_1.h)("div", { className: "panel-title" }, "Correlated Multi-Signal Timeline")),
                (selected.timeline || []).length === 0 ? ((0, jsx_runtime_1.h)("div", { style: { color: 'var(--text-secondary)' } }, "No discrete timeline transitions recorded.")) : ((0, jsx_runtime_1.h)("div", { className: "timeline-list" }, (selected.timeline || []).map((t, i) => ((0, jsx_runtime_1.h)("div", { key: i, className: "timeline-item" },
                    (0, jsx_runtime_1.h)("span", { className: "mono", style: { color: 'var(--text-secondary)' } }, t.timestamp),
                    (0, jsx_runtime_1.h)("span", { className: `badge ${t.severity}` }, t.layer),
                    (0, jsx_runtime_1.h)("div", null,
                        (0, jsx_runtime_1.h)("strong", { className: "mono" }, t.event_type),
                        " (",
                        t.object_ref,
                        "): ",
                        t.summary))))))),
            (0, jsx_runtime_1.h)("div", { className: "panel" },
                (0, jsx_runtime_1.h)("div", { className: "panel-header" },
                    (0, jsx_runtime_1.h)("div", { className: "panel-title" }, "Recommended Engineering Next Checks")),
                (0, jsx_runtime_1.h)("ul", { className: "bullet-list" }, (selected.suggested_checks || []).map((chk, i) => ((0, jsx_runtime_1.h)("li", { key: i }, chk)))),
                (0, jsx_runtime_1.h)("div", { className: "panel-header", style: { marginTop: '18px' } },
                    (0, jsx_runtime_1.h)("div", { className: "panel-title" }, "Affected Infrastructure Resources")),
                (0, jsx_runtime_1.h)("div", { style: { display: 'flex', gap: '8px', flexWrap: 'wrap' } }, (selected.affected_resources || []).map((res) => ((0, jsx_runtime_1.h)("span", { key: res, className: "mono", style: {
                        background: 'var(--bg-elevated)',
                        border: '1px solid var(--border-strong)',
                        padding: '4px 10px',
                        borderRadius: '5px',
                    } }, res))))))));
}
function renderCapacityTab(cap) {
    const history = cap.historical_utilization || [];
    const meth = cap.methodology || {};
    return ((0, jsx_runtime_1.h)("div", null,
        cap.attribution_blind_spot_warning && ((0, jsx_runtime_1.h)("div", { className: "blindspot-box" },
            (0, jsx_runtime_1.h)("strong", null, "Telemetry Attribution Blind Spot:"),
            " ",
            cap.attribution_blind_spot_warning)),
        (0, jsx_runtime_1.h)("div", { className: "kpi-grid" },
            (0, jsx_runtime_1.h)("div", { className: "kpi-card" },
                (0, jsx_runtime_1.h)("div", { className: "kpi-label" }, "Physical / Allocatable GPUs"),
                (0, jsx_runtime_1.h)("div", { className: "kpi-value" },
                    cap.total_capacity_gpus,
                    " / ",
                    cap.total_allocatable_gpus),
                (0, jsx_runtime_1.h)("div", { className: "kpi-sub" },
                    "Unavailable Hardware GPUs: ",
                    cap.unavailable_hardware_gpus)),
            (0, jsx_runtime_1.h)("div", { className: "kpi-card" },
                (0, jsx_runtime_1.h)("div", { className: "kpi-label" }, "Allocated / Free Allocatable"),
                (0, jsx_runtime_1.h)("div", { className: "kpi-value" },
                    cap.total_allocated_gpus,
                    " / ",
                    cap.total_free_allocatable_gpus),
                (0, jsx_runtime_1.h)("div", { className: "kpi-sub" },
                    "Pending Queue Requests: ",
                    cap.pending_requested_gpus,
                    " GPUs")),
            (0, jsx_runtime_1.h)("div", { className: "kpi-card" },
                (0, jsx_runtime_1.h)("div", { className: "kpi-label" }, "Observed Allocated GPU-Hours"),
                (0, jsx_runtime_1.h)("div", { className: "kpi-value" },
                    cap.total_observed_allocated_gpu_hours,
                    " GPU-h"),
                (0, jsx_runtime_1.h)("div", { className: "kpi-sub" },
                    "Below ",
                    meth.util_threshold_percent,
                    "% SM threshold: ",
                    (0, jsx_runtime_1.h)("strong", null,
                        cap.observed_low_util_gpu_hours,
                        " GPU-h"))),
            (0, jsx_runtime_1.h)("div", { className: "kpi-card" },
                (0, jsx_runtime_1.h)("div", { className: "kpi-label" }, "Illustrative Sample Cost Est."),
                (0, jsx_runtime_1.h)("div", { className: "kpi-value" }, cap.optional_low_util_sample_cost_usd !== undefined && cap.optional_low_util_sample_cost_usd !== null
                    ? `$${cap.optional_low_util_sample_cost_usd.toFixed(2)}`
                    : 'Disabled'),
                (0, jsx_runtime_1.h)("div", { className: "kpi-sub" },
                    "Sample rate: $",
                    meth.optional_sample_hourly_rate_usd?.toFixed(2) || '0.00',
                    "/GPU-h (Estimate only \u2014 NOT realized savings)"))),
        (0, jsx_runtime_1.h)("div", { className: "panel" },
            (0, jsx_runtime_1.h)("div", { className: "panel-header" },
                (0, jsx_runtime_1.h)("div", { className: "panel-title" },
                    "Historical Cluster GPU Utilization vs. Framebuffer VRAM Residency (",
                    history.length,
                    " time-series buckets)"),
                (0, jsx_runtime_1.h)("span", { className: "mono", style: { fontSize: '12px', color: 'var(--text-secondary)' } }, "Blue = Mean SM Compute Util (%) \u00B7 Emerald = Mean Framebuffer VRAM Used (%)")),
            history.length < 3 ? ((0, jsx_runtime_1.h)("div", { className: "empty-state" },
                (0, jsx_runtime_1.h)("strong", null, "Insufficient Historical Samples for Chart Rendering"),
                (0, jsx_runtime_1.h)("p", { style: { marginTop: '4px' } }, "GPU Lens renders historical charts only when at least 3 fresh time-series buckets exist."))) : (renderUtilizationSVGChart(history, meth.util_threshold_percent || 15))),
        (0, jsx_runtime_1.h)("div", { className: "panel" },
            (0, jsx_runtime_1.h)("div", { className: "panel-header" },
                (0, jsx_runtime_1.h)("div", { className: "panel-title" },
                    "Persistently Low-Utilization Allocation Windows (",
                    (cap.low_utilization_workloads || []).length,
                    ")"),
                (0, jsx_runtime_1.h)("span", { className: "badge info" },
                    "Threshold < ",
                    meth.util_threshold_percent,
                    "% over >= ",
                    meth.min_window_minutes,
                    "m")),
            (cap.low_utilization_workloads || []).length === 0 ? ((0, jsx_runtime_1.h)("div", { className: "empty-state" },
                (0, jsx_runtime_1.h)("strong", null, "No Persistently Low-Utilization Workloads Observed"),
                (0, jsx_runtime_1.h)("p", { style: { marginTop: '4px' } },
                    "All mapped running workloads either exceed the ",
                    meth.util_threshold_percent,
                    "% SM compute threshold or lack complete pod attribution labels."))) : ((0, jsx_runtime_1.h)("div", null,
                (0, jsx_runtime_1.h)("table", { className: "data-table" },
                    (0, jsx_runtime_1.h)("thead", null,
                        (0, jsx_runtime_1.h)("tr", null,
                            (0, jsx_runtime_1.h)("th", null, "Workload Pod & Owner"),
                            (0, jsx_runtime_1.h)("th", null, "Node & GPUs"),
                            (0, jsx_runtime_1.h)("th", null, "Window & Samples"),
                            (0, jsx_runtime_1.h)("th", null, "SM Compute Util"),
                            (0, jsx_runtime_1.h)("th", null, "Mem Copy & VRAM Used"),
                            (0, jsx_runtime_1.h)("th", null, "Low-Util GPU-Hours"),
                            (0, jsx_runtime_1.h)("th", null, "Workload Context & Follow-Up Checklist"))),
                    (0, jsx_runtime_1.h)("tbody", null, (cap.low_utilization_workloads || []).map((w) => ((0, jsx_runtime_1.h)("tr", { key: `${w.namespace}/${w.pod_name}` },
                        (0, jsx_runtime_1.h)("td", null,
                            (0, jsx_runtime_1.h)("div", { className: "mono", style: { fontWeight: 700 } },
                                w.namespace,
                                "/",
                                w.pod_name),
                            (0, jsx_runtime_1.h)("div", { style: { fontSize: '11.5px', color: 'var(--text-secondary)' } }, w.owner_ref)),
                        (0, jsx_runtime_1.h)("td", { className: "mono" },
                            (0, jsx_runtime_1.h)("div", null, w.node_name),
                            (0, jsx_runtime_1.h)("div", { style: { fontSize: '11px', color: 'var(--text-secondary)' } },
                                w.allocated_gpus,
                                " GPU(s): ",
                                (w.assigned_gpu_uuids || []).join(', '))),
                        (0, jsx_runtime_1.h)("td", { className: "mono" },
                            (0, jsx_runtime_1.h)("div", null,
                                w.observation_window_minutes,
                                " min"),
                            (0, jsx_runtime_1.h)("div", { style: { fontSize: '11px', color: 'var(--text-secondary)' } },
                                w.sample_count,
                                " samples (last: ",
                                w.last_data_update,
                                ")")),
                        (0, jsx_runtime_1.h)("td", { className: "mono" },
                            (0, jsx_runtime_1.h)("strong", { style: { color: '#fcd34d' } },
                                w.mean_gpu_util_percent,
                                "% mean"),
                            (0, jsx_runtime_1.h)("div", { style: { fontSize: '11px', color: 'var(--text-secondary)' } },
                                "min ",
                                w.min_gpu_util_percent,
                                "% / max ",
                                w.max_gpu_util_percent,
                                "%")),
                        (0, jsx_runtime_1.h)("td", { className: "mono" },
                            (0, jsx_runtime_1.h)("div", null,
                                "VRAM: ",
                                w.mean_fb_used_mib,
                                " MiB (",
                                w.mean_fb_used_percent,
                                "%)"),
                            (0, jsx_runtime_1.h)("div", { style: { fontSize: '11px', color: 'var(--text-secondary)' } },
                                "MemCopy: ",
                                w.mean_mem_copy_util_percent,
                                "%")),
                        (0, jsx_runtime_1.h)("td", { className: "mono" },
                            (0, jsx_runtime_1.h)("div", null,
                                w.low_util_gpu_hours,
                                " GPU-h"),
                            w.optional_sample_cost_estimate_usd !== undefined && ((0, jsx_runtime_1.h)("div", { style: { fontSize: '11px', color: 'var(--text-secondary)' } },
                                "Sample est: $",
                                w.optional_sample_cost_estimate_usd.toFixed(2)))),
                        (0, jsx_runtime_1.h)("td", { style: { maxWidth: '380px' } },
                            (0, jsx_runtime_1.h)("div", { style: { fontWeight: 600, fontSize: '12px', color: '#fde68a', marginBottom: '4px' } }, w.workload_context_note),
                            (0, jsx_runtime_1.h)("ul", { className: "bullet-list", style: { fontSize: '11.5px', color: 'var(--text-secondary)' } }, (w.follow_up_checklist || []).map((item, idx) => ((0, jsx_runtime_1.h)("li", { key: idx }, item)))))))))))),
            (0, jsx_runtime_1.h)("div", { className: "disclaimer-box" },
                (0, jsx_runtime_1.h)("strong", null, "Non-Waste Engineering Policy:"),
                " ",
                meth.non_waste_disclaimer)),
        (0, jsx_runtime_1.h)("div", { className: "panel" },
            (0, jsx_runtime_1.h)("div", { className: "panel-header" },
                (0, jsx_runtime_1.h)("div", { className: "panel-title" }, "Transparent Capacity & Low-Utilization Calculation Methodology")),
            (0, jsx_runtime_1.h)("p", { className: "mono", style: { marginBottom: '10px', color: '#7dd3fc' } }, meth.formula_summary),
            (0, jsx_runtime_1.h)("ul", { className: "bullet-list", style: { marginBottom: '10px' } }, (meth.step_by_step_calculation || []).map((s, i) => ((0, jsx_runtime_1.h)("li", { key: i }, s)))),
            (0, jsx_runtime_1.h)("p", { style: { fontSize: '12.5px', color: 'var(--text-secondary)' } },
                (0, jsx_runtime_1.h)("strong", null, "Unmapped Telemetry Policy:"),
                " ",
                meth.missing_mapping_policy),
            meth.optional_cost_estimate_label && ((0, jsx_runtime_1.h)("p", { style: { fontSize: '12.5px', color: '#fcd34d', marginTop: '6px' } },
                (0, jsx_runtime_1.h)("strong", null, "Sample Rate Estimate Notice:"),
                " ",
                meth.optional_cost_estimate_label)))));
}
function renderUtilizationSVGChart(points, threshold) {
    const width = 920;
    const height = 210;
    const padL = 48;
    const padR = 24;
    const padT = 18;
    const padB = 34;
    const plotW = width - padL - padR;
    const plotH = height - padT - padB;
    const xFor = (idx) => points.length <= 1 ? padL : padL + (idx / (points.length - 1)) * plotW;
    const yFor = (pct) => padT + plotH - (Math.max(0, Math.min(100, pct)) / 100) * plotH;
    const utilPath = points
        .map((p, i) => `${i === 0 ? 'M' : 'L'} ${xFor(i).toFixed(1)} ${yFor(p.mean_gpu_util_percent).toFixed(1)}`)
        .join(' ');
    const fbPath = points
        .map((p, i) => `${i === 0 ? 'M' : 'L'} ${xFor(i).toFixed(1)} ${yFor(p.mean_fb_used_percent).toFixed(1)}`)
        .join(' ');
    const threshY = yFor(threshold);
    return ((0, jsx_runtime_1.h)("div", { style: { overflowX: 'auto' } },
        (0, jsx_runtime_1.h)("svg", { viewBox: `0 0 ${width} ${height}`, style: { width: '100%', maxHeight: '230px' } },
            [0, 25, 50, 75, 100].map((v) => {
                const y = yFor(v);
                return ((0, jsx_runtime_1.h)("g", { key: v },
                    (0, jsx_runtime_1.h)("line", { x1: padL, y1: y, x2: width - padR, y2: y, stroke: "#1e293b", strokeWidth: "1" }),
                    (0, jsx_runtime_1.h)("text", { x: padL - 8, y: y + 4, textAnchor: "end", fill: "#64748b", fontSize: "10" },
                        v,
                        "%")));
            }),
            (0, jsx_runtime_1.h)("line", { x1: padL, y1: threshY, x2: width - padR, y2: threshY, stroke: "#f59e0b", strokeDasharray: "4 4", strokeWidth: "1.2" }),
            (0, jsx_runtime_1.h)("text", { x: width - padR - 4, y: threshY - 5, textAnchor: "end", fill: "#f59e0b", fontSize: "10" },
                "Low-Util Review Threshold (",
                threshold,
                "%)"),
            (0, jsx_runtime_1.h)("path", { d: fbPath, fill: "none", stroke: "#10b981", strokeWidth: "2.2" }),
            (0, jsx_runtime_1.h)("path", { d: utilPath, fill: "none", stroke: "#38bdf8", strokeWidth: "2.5" }),
            points.map((p, i) => ((0, jsx_runtime_1.h)("g", { key: i },
                (0, jsx_runtime_1.h)("circle", { cx: xFor(i), cy: yFor(p.mean_gpu_util_percent), r: "3.2", fill: "#38bdf8" }),
                (0, jsx_runtime_1.h)("circle", { cx: xFor(i), cy: yFor(p.mean_fb_used_percent), r: "2.8", fill: "#10b981" }),
                (i === 0 || i === points.length - 1 || i % 2 === 0) && ((0, jsx_runtime_1.h)("text", { x: xFor(i), y: height - 10, textAnchor: "middle", fill: "#94a3b8", fontSize: "10" },
                    p.timestamp.slice(11, 16),
                    "Z"))))))));
}
function renderSourcesTab(overview, sourcesDetail) {
    const sources = overview.sources || [];
    const mappings = sourcesDetail?.metric_mappings || {};
    const exported = sourcesDetail?.exported_metrics || {};
    return ((0, jsx_runtime_1.h)("div", null,
        (0, jsx_runtime_1.h)("div", { className: "panel" },
            (0, jsx_runtime_1.h)("div", { className: "panel-header" },
                (0, jsx_runtime_1.h)("div", { className: "panel-title" }, "Configured Telemetry & Cluster State Sources"),
                (0, jsx_runtime_1.h)("span", { className: "badge healthy" }, "Read-Only Transport Enforced")),
            (0, jsx_runtime_1.h)("table", { className: "data-table" },
                (0, jsx_runtime_1.h)("thead", null,
                    (0, jsx_runtime_1.h)("tr", null,
                        (0, jsx_runtime_1.h)("th", null, "Source Name & ID"),
                        (0, jsx_runtime_1.h)("th", null, "Kind / Mode"),
                        (0, jsx_runtime_1.h)("th", null, "Endpoint"),
                        (0, jsx_runtime_1.h)("th", null, "Availability & Freshness"),
                        (0, jsx_runtime_1.h)("th", null, "Exported Metrics"),
                        (0, jsx_runtime_1.h)("th", null, "Notes & Errors"))),
                (0, jsx_runtime_1.h)("tbody", null, sources.map((s) => ((0, jsx_runtime_1.h)("tr", { key: s.id },
                    (0, jsx_runtime_1.h)("td", null,
                        (0, jsx_runtime_1.h)("div", { style: { fontWeight: 700 } }, s.name),
                        (0, jsx_runtime_1.h)("div", { className: "mono", style: { color: 'var(--text-secondary)', fontSize: '11.5px' } }, s.id)),
                    (0, jsx_runtime_1.h)("td", null,
                        (0, jsx_runtime_1.h)("span", { className: "badge info" }, s.kind),
                        ' ',
                        (0, jsx_runtime_1.h)("span", { className: "badge contextual" }, s.mode)),
                    (0, jsx_runtime_1.h)("td", { className: "mono" }, s.endpoint),
                    (0, jsx_runtime_1.h)("td", null,
                        (0, jsx_runtime_1.h)("span", { className: `badge ${s.available && !s.is_stale ? 'healthy' : 'critical'}` }, s.available && !s.is_stale ? 'Available & Fresh' : 'Stale / Unavailable'),
                        (0, jsx_runtime_1.h)("div", { className: "mono", style: { fontSize: '11.5px', marginTop: '4px', color: 'var(--text-secondary)' } },
                            "Last success: ",
                            s.last_success_at,
                            " (",
                            s.staleness_seconds,
                            "s ago)")),
                    (0, jsx_runtime_1.h)("td", { className: "mono" },
                        (0, jsx_runtime_1.h)("div", null,
                            "Required: ",
                            s.exported_required_count),
                        (0, jsx_runtime_1.h)("div", null,
                            "Optional: ",
                            s.exported_optional_count),
                        (s.missing_optional || []).length > 0 && ((0, jsx_runtime_1.h)("div", { style: { fontSize: '11px', color: 'var(--text-secondary)' } },
                            "Unexported optional: ",
                            (s.missing_optional || []).join(', ')))),
                    (0, jsx_runtime_1.h)("td", { style: { maxWidth: '340px', fontSize: '12px' } },
                        s.last_error && ((0, jsx_runtime_1.h)("div", { className: "mono", style: { color: '#fca5a5', marginBottom: '4px' } },
                            "Error: ",
                            s.last_error)),
                        (0, jsx_runtime_1.h)("div", { style: { color: 'var(--text-secondary)' } }, s.notes)))))))),
        (0, jsx_runtime_1.h)("div", { className: "panel" },
            (0, jsx_runtime_1.h)("div", { className: "panel-header" },
                (0, jsx_runtime_1.h)("div", { className: "panel-title" }, "Configurable DCGM Exporter Metric & Label Mappings")),
            (0, jsx_runtime_1.h)("div", { className: "section-grid-2" },
                (0, jsx_runtime_1.h)("div", null,
                    (0, jsx_runtime_1.h)("h4", { style: { marginBottom: '8px', color: '#7dd3fc' } }, "Active Metric Field Mappings"),
                    (0, jsx_runtime_1.h)("table", { className: "data-table" },
                        (0, jsx_runtime_1.h)("thead", null,
                            (0, jsx_runtime_1.h)("tr", null,
                                (0, jsx_runtime_1.h)("th", null, "Canonical Signal"),
                                (0, jsx_runtime_1.h)("th", null, "Configured Prometheus / DCGM Name"),
                                (0, jsx_runtime_1.h)("th", null, "Exported in Snapshot"))),
                        (0, jsx_runtime_1.h)("tbody", null, Object.entries(mappings)
                            .filter(([k]) => k.endsWith('_metric'))
                            .map(([k, v]) => {
                            const metricName = String(v);
                            const isPresent = Boolean(exported[metricName]);
                            return ((0, jsx_runtime_1.h)("tr", { key: k },
                                (0, jsx_runtime_1.h)("td", { className: "mono" }, k),
                                (0, jsx_runtime_1.h)("td", { className: "mono" }, metricName),
                                (0, jsx_runtime_1.h)("td", null,
                                    (0, jsx_runtime_1.h)("span", { className: `badge ${isPresent ? 'healthy' : 'info'}` }, isPresent ? 'Exported' : 'Optional / Not Exported'))));
                        })))),
                (0, jsx_runtime_1.h)("div", null,
                    (0, jsx_runtime_1.h)("h4", { style: { marginBottom: '8px', color: '#7dd3fc' } }, "Label Resolution Priority"),
                    (0, jsx_runtime_1.h)("p", { style: { fontSize: '12.5px', color: 'var(--text-secondary)', marginBottom: '10px' } },
                        "Supports both standard ",
                        (0, jsx_runtime_1.h)("span", { className: "mono" }, "dcgm-exporter --kubernetes"),
                        " labels and legacy",
                        ' ',
                        (0, jsx_runtime_1.h)("span", { className: "mono" }, "--use-old-namespace"),
                        " conventions:"),
                    (0, jsx_runtime_1.h)("ul", { className: "bullet-list mono", style: { fontSize: '12px' } },
                        (0, jsx_runtime_1.h)("li", null,
                            "GPU UUID Labels: ",
                            (mappings.uuid_label_candidates || []).join(', ')),
                        (0, jsx_runtime_1.h)("li", null,
                            "Node Hostname Labels: ",
                            (mappings.node_name_label_candidates || []).join(', ')),
                        (0, jsx_runtime_1.h)("li", null,
                            "Pod Name Labels: ",
                            (mappings.pod_name_label_candidates || []).join(', ')),
                        (0, jsx_runtime_1.h)("li", null,
                            "Pod Namespace Labels: ",
                            (mappings.namespace_label_candidates || []).join(', ')),
                        (0, jsx_runtime_1.h)("li", null,
                            "Container Name Labels: ",
                            (mappings.container_label_candidates || []).join(', '))))))));
}
function renderRulesTab(rulesList) {
    return ((0, jsx_runtime_1.h)("div", null, rulesList.map((r) => ((0, jsx_runtime_1.h)("div", { key: r.id, className: "panel" },
        (0, jsx_runtime_1.h)("div", { className: "panel-header" },
            (0, jsx_runtime_1.h)("div", { style: { display: 'flex', alignItems: 'center', gap: '10px' } },
                (0, jsx_runtime_1.h)("span", { className: "mono", style: { fontWeight: 800, fontSize: '15px', color: '#7dd3fc' } }, r.id),
                (0, jsx_runtime_1.h)("span", { className: "panel-title" }, r.name),
                (0, jsx_runtime_1.h)("span", { className: `badge ${r.default_severity}` },
                    "Default: ",
                    r.default_severity),
                (0, jsx_runtime_1.h)("span", { className: "badge info" }, r.category))),
        (0, jsx_runtime_1.h)("p", { style: { marginBottom: '12px', color: '#e2e8f0' } }, r.description),
        (0, jsx_runtime_1.h)("div", { className: "section-grid-2" },
            (0, jsx_runtime_1.h)("div", null,
                (0, jsx_runtime_1.h)("h4", { style: { fontSize: '12.5px', color: '#93c5fd', marginBottom: '6px' } }, "Configurable Thresholds & Observation Windows"),
                (0, jsx_runtime_1.h)("table", { className: "data-table" },
                    (0, jsx_runtime_1.h)("thead", null,
                        (0, jsx_runtime_1.h)("tr", null,
                            (0, jsx_runtime_1.h)("th", null, "Parameter"),
                            (0, jsx_runtime_1.h)("th", null, "Active Value"),
                            (0, jsx_runtime_1.h)("th", null, "Purpose"))),
                    (0, jsx_runtime_1.h)("tbody", null, (r.thresholds || []).map((t) => ((0, jsx_runtime_1.h)("tr", { key: t.key },
                        (0, jsx_runtime_1.h)("td", { className: "mono" }, t.key),
                        (0, jsx_runtime_1.h)("td", { className: "mono", style: { fontWeight: 700 } },
                            t.value,
                            " ",
                            t.unit),
                        (0, jsx_runtime_1.h)("td", { style: { fontSize: '12px', color: 'var(--text-secondary)' } }, t.description))))))),
            (0, jsx_runtime_1.h)("div", null,
                (0, jsx_runtime_1.h)("h4", { style: { fontSize: '12.5px', color: '#93c5fd', marginBottom: '6px' } }, "Required & Optional Input Signals"),
                (0, jsx_runtime_1.h)("div", { style: { marginBottom: '8px', fontSize: '12.5px' } },
                    (0, jsx_runtime_1.h)("strong", null, "Required:"),
                    ' ',
                    (0, jsx_runtime_1.h)("span", { className: "mono" }, (r.required_signals || []).join(', '))),
                (r.optional_signals || []).length > 0 && ((0, jsx_runtime_1.h)("div", { style: { marginBottom: '8px', fontSize: '12.5px' } },
                    (0, jsx_runtime_1.h)("strong", null, "Optional (used when exported):"),
                    ' ',
                    (0, jsx_runtime_1.h)("span", { className: "mono" }, (r.optional_signals || []).join(', ')))),
                (0, jsx_runtime_1.h)("h4", { style: { fontSize: '12.5px', color: '#fcd34d', marginTop: '10px', marginBottom: '4px' } }, "Suppression & Inconclusive Safeguards"),
                (0, jsx_runtime_1.h)("ul", { className: "bullet-list", style: { fontSize: '12px', color: 'var(--text-secondary)' } },
                    (r.suppression_conditions || []).map((c, idx) => ((0, jsx_runtime_1.h)("li", { key: `sup-${idx}` },
                        (0, jsx_runtime_1.h)("strong", null, "Suppressed when:"),
                        " ",
                        c))),
                    (r.inconclusive_conditions || []).map((c, idx) => ((0, jsx_runtime_1.h)("li", { key: `inc-${idx}` },
                        (0, jsx_runtime_1.h)("strong", null, "Inconclusive when:"),
                        " ",
                        c)))))))))));
}

  };
  __modules["jsx-runtime"] = function(module, exports, require) {
"use strict";
// Lightweight JSX + Hooks runtime for offline compilation in air-gapped environments.
// Allows authoring standard TypeScript + TSX components with `useState`, `useEffect`, `useRef` and `useMemo`.
//
// Rendering model: every render expands the component tree into plain host nodes (elements + text) and then
// patches the existing DOM in place. Elements are reused across renders, so keyboard focus, text selection,
// scroll offsets and CSS transitions survive a re-render — a requirement for the live telemetry console, which
// re-renders every few seconds. Hook state is scoped to each mounted component instance.
Object.defineProperty(exports, "__esModule", { value: true });
exports.Fragment = void 0;
exports.h = h;
exports.useState = useState;
exports.useRef = useRef;
exports.useMemo = useMemo;
exports.useEffect = useEffect;
exports.requestRender = requestRender;
exports.mountApp = mountApp;
exports.Fragment = '__FRAGMENT__';
function h(tag, props, ...children) {
    return {
        tag,
        props: props || {},
        children,
    };
}
const instances = new Map();
let currentInstance = null;
let renderCounter = 0;
let rootFactory = null;
let rootContainer = null;
let renderScheduled = false;
let rendering = false;
function hookSlot() {
    if (!currentInstance) {
        throw new Error('Hooks can only be called while a component is rendering.');
    }
    return { inst: currentInstance, idx: currentInstance.hookIndex++ };
}
function useState(initial) {
    const { inst, idx } = hookSlot();
    if (inst.hooks.length <= idx) {
        inst.hooks[idx] = initial;
    }
    const setState = (next) => {
        const prev = inst.hooks[idx];
        const resolved = typeof next === 'function' ? next(prev) : next;
        if (resolved !== prev) {
            inst.hooks[idx] = resolved;
            if (inst.alive)
                scheduleRender();
        }
    };
    return [inst.hooks[idx], setState];
}
function useRef(initial) {
    const { inst, idx } = hookSlot();
    if (inst.hooks.length <= idx) {
        inst.hooks[idx] = { current: initial };
    }
    return inst.hooks[idx];
}
function useMemo(factory, deps) {
    const { inst, idx } = hookSlot();
    const slot = inst.hooks[idx];
    if (slot && slot.deps.length === deps.length && !deps.some((d, i) => d !== slot.deps[i])) {
        return slot.value;
    }
    const value = factory();
    inst.hooks[idx] = { deps, value };
    return value;
}
// Runs `effect` after the render in which `deps` changed (or after every render when `deps` is omitted).
// A function returned by `effect` is used as cleanup before the next run and when the component unmounts.
function useEffect(effect, deps) {
    const { inst, idx } = hookSlot();
    let slot = inst.hooks[idx];
    if (!slot) {
        slot = { deps: undefined, cleanup: undefined, hasRun: false };
        inst.hooks[idx] = slot;
    }
    const prevDeps = slot.deps;
    let changed = true;
    if (slot.hasRun && deps && prevDeps && deps.length === prevDeps.length) {
        changed = deps.some((d, i) => d !== prevDeps[i]);
    }
    if (changed) {
        slot.deps = deps;
        slot.hasRun = true;
        const target = slot;
        setTimeout(() => {
            if (!inst.alive)
                return;
            runCleanup(target);
            const result = effect();
            target.cleanup = typeof result === 'function' ? result : undefined;
        }, 0);
    }
}
function runCleanup(slot) {
    if (slot.cleanup) {
        const fn = slot.cleanup;
        slot.cleanup = undefined;
        try {
            fn();
        }
        catch (err) {
            console.error('effect cleanup failed', err);
        }
    }
}
function unmountInstance(inst) {
    inst.alive = false;
    for (const slot of inst.hooks) {
        if (slot && typeof slot === 'object' && 'hasRun' in slot && 'cleanup' in slot) {
            runCleanup(slot);
        }
    }
}
function scheduleRender() {
    if (renderScheduled)
        return;
    renderScheduled = true;
    Promise.resolve().then(() => {
        renderScheduled = false;
        performRender();
    });
}
// Lets non-component code (for example a polling data store) ask for a re-render after it mutated its state.
function requestRender() {
    scheduleRender();
}
// ---------------------------------------------------------------------------------------------
// Expansion: component tree -> host tree
// ---------------------------------------------------------------------------------------------
const EL = 0;
const TEXT = 1;
const NO_PROPS = {};
const NO_CHILDREN = [];
function textNode(text) {
    return { kind: TEXT, tag: '', props: NO_PROPS, children: NO_CHILDREN, key: undefined, text, svg: false, dom: null };
}
const SVG_NS = 'http://www.w3.org/2000/svg';
const SVG_TAGS = new Set([
    'svg', 'path', 'line', 'rect', 'circle', 'ellipse', 'g', 'text', 'tspan', 'polyline', 'polygon', 'defs',
    'linearGradient', 'radialGradient', 'stop', 'clipPath', 'mask', 'pattern', 'use', 'symbol', 'marker',
]);
const SVG_ATTR_MAP = {
    viewBox: 'viewBox',
    strokeWidth: 'stroke-width',
    strokeDasharray: 'stroke-dasharray',
    strokeDashoffset: 'stroke-dashoffset',
    strokeLinecap: 'stroke-linecap',
    strokeLinejoin: 'stroke-linejoin',
    strokeOpacity: 'stroke-opacity',
    fillOpacity: 'fill-opacity',
    textAnchor: 'text-anchor',
    dominantBaseline: 'dominant-baseline',
    fontSize: 'font-size',
    fontWeight: 'font-weight',
    fontFamily: 'font-family',
    clipPath: 'clip-path',
    stopColor: 'stop-color',
    stopOpacity: 'stop-opacity',
    vectorEffect: 'vector-effect',
    pointerEvents: 'pointer-events',
};
function childPathSegment(child, index) {
    if (child && typeof child === 'object' && !Array.isArray(child) && child.props && child.props.key != null) {
        return 'k:' + String(child.props.key);
    }
    return String(index);
}
function expand(child, path, out, isSvg) {
    if (child === null || child === undefined || child === false || child === true) {
        out.push(textNode(''));
        return;
    }
    if (typeof child === 'string' || typeof child === 'number') {
        out.push(textNode(String(child)));
        return;
    }
    if (Array.isArray(child)) {
        for (let i = 0; i < child.length; i++) {
            expand(child[i], path + '/' + childPathSegment(child[i], i), out, isSvg);
        }
        return;
    }
    if (child.tag === exports.Fragment) {
        for (let i = 0; i < child.children.length; i++) {
            expand(child.children[i], path + '/' + childPathSegment(child.children[i], i), out, isSvg);
        }
        return;
    }
    if (typeof child.tag === 'function') {
        const fn = child.tag;
        let inst = instances.get(path);
        if (!inst || inst.fn !== fn) {
            if (inst)
                unmountInstance(inst);
            inst = { fn, hooks: [], hookIndex: 0, seenInRender: 0, alive: true };
            instances.set(path, inst);
        }
        inst.seenInRender = renderCounter;
        inst.hookIndex = 0;
        const outer = currentInstance;
        currentInstance = inst;
        let rendered;
        try {
            rendered = fn({ ...child.props, children: child.children });
        }
        catch (err) {
            // Contain the failure to this component so the rest of the console (and its state) keeps working.
            console.error(`render failed in <${fn.name || 'component'}>`, err);
            rendered = renderErrorTree(err, fn.name);
        }
        finally {
            currentInstance = outer;
        }
        expand(rendered, path + '>', out, isSvg);
        return;
    }
    const tag = String(child.tag);
    const svg = isSvg || SVG_TAGS.has(tag);
    const node = {
        kind: EL,
        tag,
        props: child.props || NO_PROPS,
        children: [],
        key: child.props ? child.props.key : undefined,
        text: '',
        svg,
        dom: null,
    };
    const childSvg = svg && tag !== 'foreignObject';
    for (let i = 0; i < child.children.length; i++) {
        expand(child.children[i], path + '/' + childPathSegment(child.children[i], i), node.children, childSvg);
    }
    out.push(node);
}
function handlersOf(el) {
    const anyEl = el;
    if (!anyEl.__glHandlers)
        anyEl.__glHandlers = {};
    return anyEl.__glHandlers;
}
function setHandler(el, evName, fn) {
    const map = handlersOf(el);
    if (!(evName in map)) {
        el.addEventListener(evName, (ev) => {
            const current = handlersOf(el)[evName];
            if (current)
                current.call(el, ev);
        });
    }
    map[evName] = fn;
}
function isHandlerProp(key, value) {
    return key.length > 2 && key.charCodeAt(0) === 111 && key.charCodeAt(1) === 110 && typeof value === 'function';
}
function applyProp(el, key, value, svg, isUpdate) {
    if (key === 'ref')
        return;
    if (key === 'className') {
        el.setAttribute('class', String(value));
    }
    else if (key === 'style' && typeof value === 'object') {
        Object.assign(el.style, value);
    }
    else if (isHandlerProp(key, value)) {
        setHandler(el, key.slice(2).toLowerCase(), value);
    }
    else if (!svg && key === 'value' && 'value' in el) {
        const str = String(value);
        const control = el;
        if (!isUpdate || control.value !== str)
            control.value = str;
        if (el.getAttribute('value') !== str)
            el.setAttribute('value', str);
    }
    else if (!svg && key === 'selected' && value === true) {
        el.selected = true;
        el.setAttribute('selected', 'selected');
    }
    else if (!svg && key === 'checked' && 'checked' in el) {
        el.checked = Boolean(value);
        if (value)
            el.setAttribute('checked', 'checked');
        else
            el.removeAttribute('checked');
    }
    else {
        const attrName = svg && SVG_ATTR_MAP[key] ? SVG_ATTR_MAP[key] : key;
        const str = String(value);
        if (!isUpdate || el.getAttribute(attrName) !== str)
            el.setAttribute(attrName, str);
    }
}
function removeProp(el, key, oldValue, svg) {
    if (key === 'ref')
        return;
    if (key === 'className') {
        el.removeAttribute('class');
    }
    else if (key === 'style' && typeof oldValue === 'object') {
        el.removeAttribute('style');
    }
    else if (isHandlerProp(key, oldValue)) {
        setHandler(el, key.slice(2).toLowerCase(), undefined);
    }
    else if (!svg && key === 'selected') {
        el.selected = false;
        el.removeAttribute('selected');
    }
    else if (!svg && key === 'checked' && 'checked' in el) {
        el.checked = false;
        el.removeAttribute('checked');
    }
    else {
        el.removeAttribute(svg && SVG_ATTR_MAP[key] ? SVG_ATTR_MAP[key] : key);
    }
}
function isAbsent(v) {
    return v === undefined || v === null || v === false;
}
function createDom(node) {
    if (node.kind === TEXT) {
        return document.createTextNode(node.text);
    }
    const el = node.svg ? document.createElementNS(SVG_NS, node.tag) : document.createElement(node.tag);
    for (const k in node.props) {
        const v = node.props[k];
        if (isAbsent(v))
            continue;
        applyProp(el, k, v, node.svg, false);
    }
    patchChildren(el, NO_CHILDREN, node.children);
    finishElement(el, node);
    return el;
}
function finishElement(el, node) {
    if (node.tag === 'select' && node.props.value !== undefined) {
        const str = String(node.props.value);
        if (el.value !== str)
            el.value = str;
    }
    if (typeof node.props.ref === 'function') {
        node.props.ref(el);
    }
}
function patchProps(el, oldProps, newProps, svg) {
    for (const k in oldProps) {
        if (isAbsent(oldProps[k]))
            continue;
        if (!(k in newProps) || isAbsent(newProps[k])) {
            removeProp(el, k, oldProps[k], svg);
        }
    }
    for (const k in newProps) {
        const nv = newProps[k];
        if (isAbsent(nv))
            continue;
        const ov = oldProps[k];
        if (k === 'style' && typeof nv === 'object') {
            const style = el.style;
            if (ov && typeof ov === 'object') {
                for (const sk in ov) {
                    if (!(sk in nv))
                        style[sk] = '';
                }
                for (const sk in nv) {
                    if (ov[sk] !== nv[sk])
                        style[sk] = nv[sk];
                }
            }
            else {
                Object.assign(style, nv);
            }
            continue;
        }
        if (isHandlerProp(k, nv)) {
            if (ov !== nv)
                setHandler(el, k.slice(2).toLowerCase(), nv);
            continue;
        }
        if (ov === nv && k !== 'value' && k !== 'checked' && k !== 'selected')
            continue;
        applyProp(el, k, nv, svg, true);
    }
}
function sameKind(a, b) {
    return a.kind === b.kind && a.tag === b.tag && a.svg === b.svg;
}
function patchChildren(parent, oldKids, newKids) {
    let keyed = null;
    for (let i = 0; i < oldKids.length; i++) {
        const o = oldKids[i];
        if (o.kind === EL && o.key != null) {
            if (!keyed)
                keyed = new Map();
            if (!keyed.has(o.key))
                keyed.set(o.key, o);
        }
    }
    const matches = new Array(newKids.length);
    let used = null;
    for (let i = 0; i < newKids.length; i++) {
        const n = newKids[i];
        let match = null;
        if (keyed && n.kind === EL && n.key != null) {
            const cand = keyed.get(n.key);
            if (cand && sameKind(cand, n) && !(used && used.has(cand)))
                match = cand;
        }
        if (!match && i < oldKids.length) {
            const cand = oldKids[i];
            const keyCompatible = cand.kind === TEXT || cand.key == null || cand.key === n.key;
            if (keyCompatible && sameKind(cand, n) && !(used && used.has(cand)))
                match = cand;
        }
        if (match) {
            if (!used)
                used = new Set();
            used.add(match);
        }
        matches[i] = match;
    }
    for (let i = 0; i < oldKids.length; i++) {
        const o = oldKids[i];
        if (!(used && used.has(o)) && o.dom && o.dom.parentNode === parent) {
            parent.removeChild(o.dom);
        }
    }
    let cursor = parent.firstChild;
    for (let i = 0; i < newKids.length; i++) {
        const n = newKids[i];
        const m = matches[i];
        let dom;
        if (m && m.dom) {
            dom = m.dom;
            n.dom = dom;
            if (n.kind === TEXT) {
                if (m.text !== n.text)
                    dom.nodeValue = n.text;
            }
            else {
                patchProps(dom, m.props, n.props, n.svg);
                patchChildren(dom, m.children, n.children);
                finishElement(dom, n);
            }
        }
        else {
            dom = createDom(n);
            n.dom = dom;
        }
        if (dom === cursor) {
            cursor = cursor.nextSibling;
        }
        else {
            parent.insertBefore(dom, cursor);
        }
    }
}
// ---------------------------------------------------------------------------------------------
// Root render loop
// ---------------------------------------------------------------------------------------------
let previousTree = [];
function renderErrorTree(err, where) {
    const message = err && err.message ? String(err.message) : String(err);
    return h('div', { className: 'main-container' }, h('div', { className: 'panel', style: { borderColor: 'var(--status-critical)' } }, h('div', { className: 'panel-title', style: { color: '#fca5a5' } }, where ? `Console Render Error in ${where}` : 'Console Render Error'), h('p', { className: 'mono', style: { marginTop: '8px' } }, message), h('p', { style: { marginTop: '8px', color: 'var(--text-secondary)' } }, 'Reload the page to retry. The backend keeps running.')));
}
function performRender() {
    if (!rootContainer || !rootFactory || rendering)
        return;
    rendering = true;
    try {
        renderCounter++;
        let next = [];
        try {
            expand(rootFactory(), 'r', next, false);
        }
        catch (err) {
            console.error('render failed', err);
            currentInstance = null;
            next = [];
            expand(renderErrorTree(err), 'error', next, false);
        }
        for (const [path, inst] of instances) {
            if (inst.seenInRender !== renderCounter) {
                unmountInstance(inst);
                instances.delete(path);
            }
        }
        patchChildren(rootContainer, previousTree, next);
        previousTree = next;
    }
    finally {
        rendering = false;
    }
}
function mountApp(factory, container) {
    rootFactory = factory;
    rootContainer = container;
    container.innerHTML = '';
    previousTree = [];
    performRender();
}

  };
  __modules["live/api"] = function(module, exports, require) {
"use strict";
Object.defineProperty(exports, "__esModule", { value: true });
exports.saveThresholds = exports.saveLiveConfig = exports.recheckLive = exports.stopLive = exports.startLive = void 0;
exports.fetchLiveState = fetchLiveState;
async function request(method, path, body) {
    const init = { method, headers: { Accept: 'application/json' }, cache: 'no-store' };
    if (method === 'POST') {
        init.headers['Content-Type'] = 'application/json';
        init.body = JSON.stringify(body || {});
    }
    const resp = await fetch(path, init);
    const text = await resp.text();
    if (!resp.ok) {
        let msg = text;
        try {
            msg = JSON.parse(text).error || text;
        }
        catch {
            /* keep raw text */
        }
        throw new Error(msg || `HTTP ${resp.status}`);
    }
    return JSON.parse(text);
}
function fetchLiveState(since, activitySince) {
    return request('GET', `/api/v1/live/state?since=${since}&activity_since=${activitySince}`);
}
const startLive = (intervalSeconds) => request('POST', '/api/v1/live/start', intervalSeconds ? { interval_seconds: intervalSeconds } : {});
exports.startLive = startLive;
const stopLive = () => request('POST', '/api/v1/live/stop');
exports.stopLive = stopLive;
const recheckLive = () => request('POST', '/api/v1/live/recheck');
exports.recheckLive = recheckLive;
const saveLiveConfig = (patch) => request('POST', '/api/v1/live/config', patch);
exports.saveLiveConfig = saveLiveConfig;
const saveThresholds = (overrides, reset) => request('POST', '/api/v1/live/thresholds', reset ? { reset: true } : { overrides: overrides || {} });
exports.saveThresholds = saveThresholds;

  };
  __modules["live/LiveConsole"] = function(module, exports, require) {
"use strict";
Object.defineProperty(exports, "__esModule", { value: true });
exports.LiveConsole = LiveConsole;
// "Go Live" console — part 2 of the UI. Part 1 (the simulated demo in App.tsx) is not touched by anything here.
const jsx_runtime_1 = require("../jsx-runtime");
const store = require("./store");
const store_1 = require("./store");
const App_1 = require("../App");
const ui_1 = require("./ui");
const views_a_1 = require("./views_a");
const views_b_1 = require("./views_b");
const TABS = [
    ['command', 'Command Center'],
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
function initialTab() {
    try {
        const t = new URLSearchParams(window.location.search).get('live_tab') || '';
        return TABS.some((x) => x[0] === t) ? t : 'command';
    }
    catch {
        return 'command';
    }
}
function LiveConsole(props) {
    const [tab, setTabRaw] = (0, jsx_runtime_1.useState)(initialTab());
    const [gpuFilters, setGpuFilters] = (0, jsx_runtime_1.useState)({ q: '', node: 'all', sort: 'index', status: 'all' });
    const [openGpu, setOpenGpu] = (0, jsx_runtime_1.useState)('');
    const [sev, setSev] = (0, jsx_runtime_1.useState)('all');
    const [cat, setCat] = (0, jsx_runtime_1.useState)('all');
    const [query, setQuery] = (0, jsx_runtime_1.useState)('');
    const [selected, setSelected] = (0, jsx_runtime_1.useState)(null);
    const [incView, setIncView] = (0, jsx_runtime_1.useState)('queue');
    const [conn, setConn] = (0, jsx_runtime_1.useState)(null);
    const [thDraft, setThDraft] = (0, jsx_runtime_1.useState)({});
    const [actFilter, setActFilter] = (0, jsx_runtime_1.useState)('all');
    function setTab(t) {
        setTabRaw(t);
        try {
            const u = new URL(window.location.href);
            u.searchParams.set('live_tab', t);
            window.history.replaceState(null, '', u.toString());
        }
        catch {
            /* non-critical */
        }
    }
    (0, jsx_runtime_1.useEffect)(() => {
        store.enter();
        return () => store.leave();
    }, []);
    const s = store_1.live.state;
    // Seed the connection form once the first state arrives (and again whenever the server config changes underneath us).
    const cfgKey = s ? JSON.stringify(s.config) : '';
    (0, jsx_runtime_1.useEffect)(() => {
        if (s)
            setConn((0, views_b_1.draftFromConfig)(s.config));
    }, [cfgKey]);
    if (!s) {
        return ((0, jsx_runtime_1.h)("div", { className: "main-container" },
            (0, jsx_runtime_1.h)("div", { className: "panel" },
                (0, jsx_runtime_1.h)("div", { className: "panel-title" }, store_1.live.error ? 'Cannot reach the GPU Lens backend' : 'Going live…'),
                (0, jsx_runtime_1.h)("p", { style: { color: 'var(--text-secondary)', marginTop: '8px' } }, store_1.live.error || 'Starting collection and checking every telemetry source (host, NVIDIA driver, DCGM, Prometheus, Kubernetes).'),
                (0, jsx_runtime_1.h)("div", { style: { marginTop: '12px', display: 'flex', gap: '8px' } },
                    (0, jsx_runtime_1.h)("button", { className: "btn", onClick: () => store.refresh() }, "Retry"),
                    (0, jsx_runtime_1.h)("button", { className: "btn", onClick: props.onExit }, "Open the simulation \u2192")))));
    }
    if (!s.status.supported) {
        return ((0, jsx_runtime_1.h)("div", { className: "main-container" },
            (0, jsx_runtime_1.h)("div", { className: "panel" },
                (0, jsx_runtime_1.h)("div", { className: "panel-title" }, "Go Live is switched off"),
                (0, jsx_runtime_1.h)("p", { style: { color: 'var(--text-secondary)', marginTop: '8px' } }, s.status.unsupported_reason || 'The live console is disabled in this build.'),
                (0, jsx_runtime_1.h)("button", { className: "btn", style: { marginTop: '12px' }, onClick: props.onExit }, "Open the simulation \u2192"))));
    }
    const ov = s.overview;
    const incidents = (ov && ov.active_incidents) || [];
    const running = s.status.state === 'running';
    const stale = running && store_1.live.lastFetchAt > 0 && Date.now() - store_1.live.lastFetchAt > Math.max(8000, s.status.interval_seconds * 4000);
    const filteredIncidents = incidents.filter((inc) => {
        if (sev !== 'all' && inc.severity !== sev)
            return false;
        if (cat !== 'all' && inc.category !== cat)
            return false;
        if (query.trim()) {
            const q = query.toLowerCase();
            const hay = `${inc.title} ${inc.summary} ${inc.rule_id} ${(inc.affected_resources || []).join(' ')}`.toLowerCase();
            if (!hay.includes(q))
                return false;
        }
        return true;
    });
    const openIncident = (inc) => {
        setSelected(inc);
        setIncView('detail');
        setTab('incidents');
    };
    const selectedNow = selected ? incidents.find((i) => i.fingerprint === selected.fingerprint) || selected : incidents[0] || null;
    async function saveConn() {
        if (!conn || !s)
            return;
        const patch = (0, views_b_1.buildConfigPatch)(conn, s.config);
        if (await store.saveConfig(patch))
            setConn(null);
    }
    async function applyThresholds() {
        const o = {};
        for (const k of Object.keys(thDraft)) {
            const v = thDraft[k];
            if (v !== '' && !Number.isNaN(Number(v)) && Number(v) !== s.thresholds[k])
                o[k] = Number(v);
        }
        if (Object.keys(o).length && (await store.saveThresholds(o, false)))
            setThDraft({});
    }
    async function resetThresholds() {
        if (await store.saveThresholds(null, true))
            setThDraft({});
    }
    let body = null;
    switch (tab) {
        case 'command':
            body = (0, views_a_1.renderCommandCenter)(store_1.live, s, setTab, props.onExit);
            break;
        case 'preflight':
            body = (0, views_a_1.renderPreflight)(store_1.live, s, () => store.recheck());
            break;
        case 'gpus':
            body = (0, views_a_1.renderGPUs)(store_1.live, s, gpuFilters, setGpuFilters, openGpu, setOpenGpu);
            break;
        case 'incidents':
            body = ((0, jsx_runtime_1.h)("div", null,
                (0, jsx_runtime_1.h)("div", { className: "lv-subnav" }, [['queue', `Queue (${incidents.length})`], ['detail', 'Detail']].map(([k, l]) => ((0, jsx_runtime_1.h)("button", { className: `lv-subtab ${incView === k ? 'active' : ''}`, onClick: () => setIncView(k) }, l)))),
                incView === 'queue'
                    ? (0, App_1.renderIncidentsTab)(filteredIncidents, sev, setSev, cat, setCat, query, setQuery, openIncident)
                    : (0, App_1.renderIncidentDetailTab)(selectedNow, incidents, setSelected),
                incidents.length === 0 && s.gpus.length === 0 && ((0, jsx_runtime_1.h)("div", { className: "blindspot-box" },
                    (0, jsx_runtime_1.h)("strong", null, "No telemetry, no verdict."),
                    " The incident list is empty because no GPU is reporting \u2014 not because the fleet is healthy."))));
            break;
        case 'capacity':
            body = ov && ov.capacity_summary && s.gpus.length ? (0, App_1.renderCapacityTab)(ov.capacity_summary) : ((0, jsx_runtime_1.h)("div", { className: "panel" },
                (0, jsx_runtime_1.h)(ui_1.Empty, { title: "Capacity needs GPU telemetry" }, "Capacity math runs as soon as a source reports GPUs.")));
            break;
        case 'workloads':
            body = (0, views_b_1.renderWorkloads)(s);
            break;
        case 'sources':
            body = (0, views_b_1.renderSources)(s, () => store.recheck(), store_1.live.busy);
            break;
        case 'host':
            body = (0, views_a_1.renderHost)(store_1.live, s);
            break;
        case 'connections':
            body = conn
                ? (0, views_b_1.renderConnections)(s, conn, setConn, saveConn, () => setConn((0, views_b_1.draftFromConfig)(s.config)), store_1.live.busy)
                : (0, jsx_runtime_1.h)("div", { className: "panel" }, "Loading\u2026");
            break;
        case 'thresholds':
            body = (0, views_b_1.renderThresholds)(s, thDraft, setThDraft, applyThresholds, resetThresholds, store_1.live.busy);
            break;
        case 'activity':
            body = (0, views_b_1.renderActivity)(store_1.live, s, actFilter, setActFilter);
            break;
        case 'rules':
            body = (0, App_1.renderRulesTab)(s.rules || []);
            break;
        default: body = null;
    }
    const pf = s.preflight;
    const counts = (ov && ov.active_incident_counts) || { critical: 0, warning: 0, inconclusive: 0 };
    const tabCount = (k) => {
        if (k === 'incidents')
            return incidents.length;
        if (k === 'gpus')
            return s.gpus.length;
        if (k === 'activity')
            return store_1.live.activity.length;
        return null;
    };
    return ((0, jsx_runtime_1.h)("div", { className: "lv-root" },
        (0, jsx_runtime_1.h)("div", { className: "lv-topbar" },
            (0, jsx_runtime_1.h)("div", { className: "lv-live-pill" },
                (0, jsx_runtime_1.h)("span", { className: `lv-pulse ${running && !stale ? 'on' : ''}` }),
                running ? (stale ? 'STALLED' : 'LIVE') : 'PAUSED'),
            (0, jsx_runtime_1.h)("span", { className: "lv-note mono" },
                s.status.runtime,
                " v",
                s.status.version,
                " \u00B7 ",
                s.status.hostname,
                " \u00B7 cycle ",
                s.status.tick,
                " \u00B7 every ",
                s.status.interval_seconds,
                "s",
                s.status.last_tick_duration_ms ? ` · last cycle ${(0, ui_1.num)(s.status.last_tick_duration_ms, 0)} ms` : ''),
            (0, jsx_runtime_1.h)("div", { style: { flex: '1' } }),
            running
                ? (0, jsx_runtime_1.h)("button", { className: "btn", disabled: store_1.live.busy !== '', onClick: () => store.stop() }, "Pause collection")
                : (0, jsx_runtime_1.h)("button", { className: "btn btn-primary", disabled: store_1.live.busy !== '', onClick: () => store.start() }, "Resume collection"),
            (0, jsx_runtime_1.h)("button", { className: "btn", disabled: store_1.live.busy !== '', onClick: () => store.recheck() }, store_1.live.busy === 'recheck' ? 'Checking…' : 'Re-check everything'),
            (0, jsx_runtime_1.h)("button", { className: "btn", onClick: () => (0, ui_1.download)(`gpu-lens-live-${new Date().toISOString().replace(/[:.]/g, '-')}.json`, store.exportSnapshot()) }, "Export JSON"),
            (0, jsx_runtime_1.h)("button", { className: "btn", onClick: props.onExit }, "Open the simulation \u2192")),
        s.status.emulated && ((0, jsx_runtime_1.h)("div", { className: "lv-emulated" },
            (0, jsx_runtime_1.h)("b", null, "EMULATED DATA"),
            " \u2014 the connected DCGM/Kubernetes endpoints are the GPU Lens ",
            (0, jsx_runtime_1.h)("span", { className: "mono" }, "Live Lab"),
            " synthetic emulator, not real hardware. Everything below is computed live, but the GPUs do not exist.")),
        store_1.live.error && ((0, jsx_runtime_1.h)("div", { className: "lv-alert error" },
            (0, jsx_runtime_1.h)("span", null,
                (0, jsx_runtime_1.h)("b", null, "Connection problem:"),
                " ",
                store_1.live.error),
            (0, jsx_runtime_1.h)("button", { className: "btn", onClick: () => store.clearMessages() }, "Dismiss"))),
        store_1.live.notice && ((0, jsx_runtime_1.h)("div", { className: "lv-alert ok" },
            (0, jsx_runtime_1.h)("span", null, store_1.live.notice),
            (0, jsx_runtime_1.h)("button", { className: "btn", onClick: () => store.clearMessages() }, "Dismiss"))),
        !running && ((0, jsx_runtime_1.h)("div", { className: "lv-alert warn" },
            (0, jsx_runtime_1.h)("span", null,
                (0, jsx_runtime_1.h)("b", null, "Collection is not running."),
                " ",
                s.status.state_reason || 'Press Resume to start collecting again.'),
            (0, jsx_runtime_1.h)("button", { className: "btn btn-primary", onClick: () => store.start() }, "Resume"))),
        (0, jsx_runtime_1.h)("header", { className: "top-header" },
            (0, jsx_runtime_1.h)("div", { className: "brand-group" },
                (0, jsx_runtime_1.h)("div", { className: "brand-logo live" }, "GL"),
                (0, jsx_runtime_1.h)("div", null,
                    (0, jsx_runtime_1.h)("div", { className: "brand-title" },
                        "GPU Lens ",
                        (0, jsx_runtime_1.h)("span", { className: "lv-live-tag" }, "LIVE")),
                    (0, jsx_runtime_1.h)("div", { className: "brand-subtitle" },
                        "Cluster: ",
                        (0, jsx_runtime_1.h)("span", { className: "mono", style: { color: '#e2e8f0' } }, (ov && ov.cluster_name) || s.status.hostname),
                        " \u00B7 read-only"))),
            (0, jsx_runtime_1.h)("nav", { className: "nav-tabs lv-tabs" }, TABS.map(([k, label]) => ((0, jsx_runtime_1.h)("button", { className: `nav-tab ${tab === k ? 'active' : ''}`, onClick: () => setTab(k) },
                label,
                tabCount(k) !== null && (0, jsx_runtime_1.h)("span", { className: "tab-count" }, tabCount(k))))))),
        (0, jsx_runtime_1.h)("main", { className: "main-container" },
            (0, jsx_runtime_1.h)("div", { className: `health-banner ${(0, views_a_1.verdictClass)(pf.verdict)}` },
                (0, jsx_runtime_1.h)("div", null,
                    (0, jsx_runtime_1.h)("div", { style: { display: 'flex', alignItems: 'center', gap: '10px', flexWrap: 'wrap' } },
                        (0, jsx_runtime_1.h)("span", { className: `badge ${(0, views_a_1.verdictClass)(pf.verdict)}` }, pf.verdict),
                        (0, jsx_runtime_1.h)("strong", { style: { fontSize: '14.5px' } }, pf.verdict_text)),
                    ov && s.gpus.length > 0 && ((0, jsx_runtime_1.h)("div", { className: "lv-note", style: { marginTop: '4px' } }, ov.fleet_health_headline))),
                (0, jsx_runtime_1.h)("div", { className: "mono", style: { fontSize: '12px' } },
                    "Critical: ",
                    counts.critical || 0,
                    " \u00B7 Warning: ",
                    counts.warning || 0,
                    " \u00B7 Inconclusive: ",
                    counts.inconclusive || 0)),
            body,
            (0, jsx_runtime_1.h)("div", { className: "lv-footer lv-note" },
                "Read-only by construction: every request GPU Lens makes is a GET/HEAD or an allow-listed nvidia-smi query. ",
                ov && (0, jsx_runtime_1.h)(ui_1.Badge, { kind: "info" }, ov.data_mode)))));
}

  };
  __modules["live/store"] = function(module, exports, require) {
"use strict";
Object.defineProperty(exports, "__esModule", { value: true });
exports.live = void 0;
exports.refresh = refresh;
exports.enter = enter;
exports.leave = leave;
exports.start = start;
exports.stop = stop;
exports.recheck = recheck;
exports.saveConfig = saveConfig;
exports.saveThresholds = saveThresholds;
exports.setInterval_ = setInterval_;
exports.clearMessages = clearMessages;
exports.exportSnapshot = exportSnapshot;
// Polling store for the live console. Lives outside the component tree so that polling survives re-renders;
// components read `live` and call `requestRender` through the helpers below.
const jsx_runtime_1 = require("../jsx-runtime");
const api = require("./api");
exports.live = {
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
};
const MAX_ACTIVITY = 500;
let timer;
let inFlight = false;
let consecutiveErrors = 0;
function absorb(next, replace) {
    const h = next.history;
    if (replace || h.epoch !== exports.live.historyEpoch) {
        exports.live.history = h.points.slice();
        exports.live.historyEpoch = h.epoch;
    }
    else if (h.points.length) {
        const lastKnown = exports.live.history.length ? exports.live.history[exports.live.history.length - 1].seq : 0;
        for (const p of h.points)
            if (p.seq > lastKnown)
                exports.live.history.push(p);
    }
    const cap = Math.max(60, h.capacity || 900);
    if (exports.live.history.length > cap)
        exports.live.history.splice(0, exports.live.history.length - cap);
    if (replace)
        exports.live.activity = next.activity.slice();
    else {
        const lastKnown = exports.live.activity.length ? exports.live.activity[exports.live.activity.length - 1].seq : 0;
        for (const a of next.activity)
            if (a.seq > lastKnown)
                exports.live.activity.push(a);
    }
    if (exports.live.activity.length > MAX_ACTIVITY)
        exports.live.activity.splice(0, exports.live.activity.length - MAX_ACTIVITY);
    exports.live.state = next;
}
function lastSeq() {
    return exports.live.history.length ? exports.live.history[exports.live.history.length - 1].seq : 0;
}
function lastActivitySeq() {
    return exports.live.activity.length ? exports.live.activity[exports.live.activity.length - 1].seq : 0;
}
async function refresh() {
    if (inFlight)
        return;
    inFlight = true;
    try {
        let next = await api.fetchLiveState(lastSeq(), lastActivitySeq());
        if (next.history.epoch !== exports.live.historyEpoch && exports.live.historyEpoch !== -1) {
            // The server rebuilt the series (new session or config change): reload everything from the start.
            next = await api.fetchLiveState(0, 0);
            absorb(next, true);
        }
        else {
            absorb(next, exports.live.historyEpoch === -1);
        }
        exports.live.error = '';
        exports.live.lastFetchAt = Date.now();
        exports.live.fetchCount++;
        consecutiveErrors = 0;
    }
    catch (err) {
        consecutiveErrors++;
        exports.live.error = err?.message || 'Could not reach the GPU Lens backend.';
    }
    finally {
        inFlight = false;
        (0, jsx_runtime_1.requestRender)();
    }
}
function nextDelayMs() {
    const s = exports.live.state?.status.interval_seconds || 2;
    let ms = Math.min(10, Math.max(1, s)) * 1000;
    if (typeof document !== 'undefined' && document.hidden)
        ms = Math.max(ms, 5000);
    if (consecutiveErrors > 0)
        ms = Math.min(15000, ms * (1 + consecutiveErrors));
    return ms;
}
function loop() {
    if (!exports.live.active)
        return;
    timer = window.setTimeout(async () => {
        await refresh();
        loop();
    }, nextDelayMs());
}
async function act(name, fn, reset = false) {
    exports.live.busy = name;
    exports.live.notice = '';
    (0, jsx_runtime_1.requestRender)();
    try {
        const next = await fn();
        absorb(next, reset || next.history.epoch !== exports.live.historyEpoch);
        exports.live.error = '';
        return true;
    }
    catch (err) {
        exports.live.notice = '';
        exports.live.error = err?.message || 'Request failed.';
        return false;
    }
    finally {
        exports.live.busy = '';
        (0, jsx_runtime_1.requestRender)();
    }
}
/** Enter the console: start collection on the server (unless the user paused it) and begin polling. */
async function enter() {
    if (exports.live.active)
        return;
    exports.live.active = true;
    exports.live.paused = false;
    exports.live.error = '';
    (0, jsx_runtime_1.requestRender)();
    await act('start', () => api.startLive(), true);
    loop();
}
function leave() {
    exports.live.active = false;
    if (timer !== undefined)
        window.clearTimeout(timer);
    timer = undefined;
    (0, jsx_runtime_1.requestRender)();
}
async function start() {
    exports.live.paused = false;
    await act('start', () => api.startLive());
}
async function stop() {
    exports.live.paused = true;
    await act('stop', () => api.stopLive());
}
async function recheck() {
    if (await act('recheck', () => api.recheckLive()))
        exports.live.notice = 'Re-checked every source just now.';
    (0, jsx_runtime_1.requestRender)();
}
async function saveConfig(patch) {
    const ok = await act('config', () => api.saveLiveConfig(patch), true);
    if (ok)
        exports.live.notice = 'Connection settings saved — the session restarted and every source was checked again.';
    (0, jsx_runtime_1.requestRender)();
    return ok;
}
async function saveThresholds(overrides, reset) {
    const ok = await act('thresholds', () => api.saveThresholds(overrides, reset));
    if (ok)
        exports.live.notice = reset ? 'Thresholds are back to their defaults.' : 'Thresholds updated — rules re-evaluated on the next cycle.';
    (0, jsx_runtime_1.requestRender)();
    return ok;
}
function setInterval_(seconds) {
    return saveConfig({ interval_seconds: seconds });
}
function clearMessages() {
    exports.live.error = '';
    exports.live.notice = '';
    (0, jsx_runtime_1.requestRender)();
}
// Export everything the console currently knows as one JSON document.
function exportSnapshot() {
    const s = exports.live.state;
    return JSON.stringify({
        exported_at: new Date().toISOString(),
        note: 'GPU Lens live snapshot. Read-only telemetry; secrets are never included.',
        state: s ? { ...s, history: { ...s.history, points: exports.live.history }, activity: exports.live.activity } : null,
    }, null, 2);
}

  };
  __modules["live/types"] = function(module, exports, require) {
"use strict";
Object.defineProperty(exports, "__esModule", { value: true });

  };
  __modules["live/ui"] = function(module, exports, require) {
"use strict";
Object.defineProperty(exports, "__esModule", { value: true });
exports.STATE_LABEL = exports.CHECK_ICON = void 0;
exports.num = num;
exports.bytesPerSec = bytesPerSec;
exports.mib = mib;
exports.duration = duration;
exports.clock = clock;
exports.ago = ago;
exports.download = download;
exports.tone = tone;
exports.Badge = Badge;
exports.Meter = Meter;
exports.Stat = Stat;
exports.Empty = Empty;
exports.LineChart = LineChart;
exports.Sparkline = Sparkline;
exports.Heatmap = Heatmap;
const jsx_runtime_1 = require("../jsx-runtime");
// ---------- formatting ----------
function num(v, digits = 0, unit = '') {
    if (v === null || v === undefined || Number.isNaN(v))
        return '—';
    return v.toFixed(digits) + unit;
}
function bytesPerSec(v) {
    if (v === null || v === undefined)
        return '—';
    const u = ['B/s', 'KB/s', 'MB/s', 'GB/s'];
    let i = 0;
    let x = v;
    while (x >= 1024 && i < u.length - 1) {
        x /= 1024;
        i++;
    }
    return `${x.toFixed(i === 0 ? 0 : 1)} ${u[i]}`;
}
function mib(v) {
    if (v === null || v === undefined)
        return '—';
    return v >= 1024 ? `${(v / 1024).toFixed(1)} GiB` : `${v.toFixed(0)} MiB`;
}
function duration(sec) {
    if (sec === null || sec === undefined)
        return '—';
    const s = Math.max(0, Math.round(sec));
    const d = Math.floor(s / 86400);
    const hh = Math.floor((s % 86400) / 3600);
    const m = Math.floor((s % 3600) / 60);
    if (d > 0)
        return `${d}d ${hh}h`;
    if (hh > 0)
        return `${hh}h ${m}m`;
    if (m > 0)
        return `${m}m ${s % 60}s`;
    return `${s}s`;
}
function clock(iso) {
    if (!iso)
        return '—';
    const d = new Date(iso);
    if (Number.isNaN(d.getTime()))
        return '—';
    return d.toLocaleTimeString([], { hour12: false });
}
function ago(iso, nowMs) {
    if (!iso)
        return 'never';
    const t = new Date(iso).getTime();
    if (Number.isNaN(t))
        return 'never';
    const s = Math.max(0, (nowMs - t) / 1000);
    return s < 2 ? 'just now' : `${duration(s)} ago`;
}
function download(filename, text, mime = 'application/json') {
    const blob = new Blob([text], { type: mime });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = filename;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    window.setTimeout(() => URL.revokeObjectURL(url), 2000);
}
// ---------- status colour helpers ----------
function tone(v, warn, crit) {
    if (v === null || v === undefined)
        return 'muted';
    if (v >= crit)
        return 'critical';
    if (v >= warn)
        return 'warning';
    return 'healthy';
}
exports.CHECK_ICON = { pass: '✓', warn: '!', fail: '✕', skip: '–', info: 'i', pending: '…' };
exports.STATE_LABEL = {
    connected: 'Connected',
    degraded: 'Degraded',
    unreachable: 'Unreachable',
    not_found: 'Not found',
    disabled: 'Off',
    pending: 'Checking…',
    error: 'Error',
};
// ---------- small components (plain functions: they hold no state) ----------
function Badge(props) {
    return (0, jsx_runtime_1.h)("span", { className: `badge ${props.kind}` }, props.children);
}
function Meter(props) {
    const max = props.max || 100;
    const v = props.value;
    const pct = v === null || v === undefined ? 0 : Math.max(0, Math.min(100, (v / max) * 100));
    const t = tone(v, props.warn ?? 1e9, props.crit ?? 1e9);
    return ((0, jsx_runtime_1.h)("div", { className: "lv-meter", title: props.label },
        (0, jsx_runtime_1.h)("div", { className: `lv-meter-fill ${t === 'muted' ? 'muted' : t}`, style: { width: `${pct}%` } })));
}
function Stat(props) {
    return ((0, jsx_runtime_1.h)("div", { className: `lv-stat ${props.kind || ''}` },
        (0, jsx_runtime_1.h)("div", { className: "lv-stat-label" }, props.label),
        (0, jsx_runtime_1.h)("div", { className: "lv-stat-value" }, props.value),
        props.sub !== undefined && props.sub !== null && (0, jsx_runtime_1.h)("div", { className: "lv-stat-sub" }, props.sub)));
}
function Empty(props) {
    return ((0, jsx_runtime_1.h)("div", { className: "empty-state" },
        (0, jsx_runtime_1.h)("div", { style: { fontWeight: 600, color: 'var(--text-primary)', marginBottom: '4px' } }, props.title),
        (0, jsx_runtime_1.h)("div", null, props.children)));
}
function niceMax(v) {
    if (v <= 0)
        return 1;
    const p = Math.pow(10, Math.floor(Math.log10(v)));
    const f = v / p;
    const n = f <= 1 ? 1 : f <= 2 ? 2 : f <= 2.5 ? 2.5 : f <= 5 ? 5 : 10;
    return n * p;
}
function LineChart(props) {
    const W = 760;
    const H = props.height || 190;
    const padL = 40;
    const padR = 12;
    const padT = 10;
    const padB = 22;
    const n = props.times.length;
    const flat = [];
    for (const s of props.series)
        for (const v of s.values)
            if (v !== null && v !== undefined)
                flat.push(v);
    if (n < 2 || flat.length === 0) {
        return ((0, jsx_runtime_1.h)("div", { className: "lv-chart-empty" }, props.empty || 'Collecting samples — the chart appears after the second refresh.'));
    }
    const yMin = props.yMin ?? 0;
    let yMax = props.yMax ?? niceMax(Math.max(...flat, props.threshold ? props.threshold.value : 0) * 1.08);
    if (yMax <= yMin)
        yMax = yMin + 1;
    const x = (i) => padL + (i / (n - 1)) * (W - padL - padR);
    const y = (v) => padT + (1 - (v - yMin) / (yMax - yMin)) * (H - padT - padB);
    const ticks = [0, 0.25, 0.5, 0.75, 1].map((f) => yMin + f * (yMax - yMin));
    const labelIdx = [0, Math.floor((n - 1) / 2), n - 1];
    const paths = props.series.map((s) => {
        let d = '';
        let pen = false;
        s.values.forEach((v, i) => {
            if (v === null || v === undefined) {
                pen = false; // a gap, never a zero
                return;
            }
            d += `${pen ? 'L' : 'M'}${x(i).toFixed(1)},${y(v).toFixed(1)} `;
            pen = true;
        });
        return d.trim();
    });
    return ((0, jsx_runtime_1.h)("div", null,
        (0, jsx_runtime_1.h)("svg", { className: "lv-chart", viewBox: `0 0 ${W} ${H}`, role: "img" },
            ticks.map((t) => ((0, jsx_runtime_1.h)("g", null,
                (0, jsx_runtime_1.h)("line", { x1: padL, x2: W - padR, y1: y(t), y2: y(t), stroke: "#263248", strokeWidth: "1" }),
                (0, jsx_runtime_1.h)("text", { x: padL - 6, y: y(t) + 3, textAnchor: "end", fontSize: "10", fill: "#64748b" }, t >= 100 || yMax > 20 ? t.toFixed(0) : t.toFixed(1))))),
            props.threshold && props.threshold.value >= yMin && props.threshold.value <= yMax && ((0, jsx_runtime_1.h)("g", null,
                (0, jsx_runtime_1.h)("line", { x1: padL, x2: W - padR, y1: y(props.threshold.value), y2: y(props.threshold.value), stroke: props.threshold.color || '#ef4444', strokeWidth: "1", strokeDasharray: "5 4" }),
                (0, jsx_runtime_1.h)("text", { x: W - padR, y: y(props.threshold.value) - 4, textAnchor: "end", fontSize: "10", fill: props.threshold.color || '#ef4444' }, props.threshold.label))),
            paths.map((d, i) => ((0, jsx_runtime_1.h)("path", { d: d, fill: "none", stroke: props.series[i].color, strokeWidth: "1.8", strokeLinejoin: "round", strokeDasharray: props.series[i].dashed ? '4 3' : undefined }))),
            labelIdx.map((i) => ((0, jsx_runtime_1.h)("text", { x: x(i), y: H - 6, textAnchor: i === 0 ? 'start' : i === n - 1 ? 'end' : 'middle', fontSize: "10", fill: "#64748b" }, clock(props.times[i]))))),
        props.series.length > 1 && ((0, jsx_runtime_1.h)("div", { className: "lv-legend" }, props.series.map((s) => ((0, jsx_runtime_1.h)("span", null,
            (0, jsx_runtime_1.h)("i", { style: { background: s.color } }),
            s.name)))))));
}
function Sparkline(props) {
    const W = props.width || 120;
    const H = props.height || 28;
    const vals = props.values.filter((v) => v !== null && v !== undefined);
    if (vals.length < 2)
        return (0, jsx_runtime_1.h)("svg", { className: "lv-spark", viewBox: `0 0 ${W} ${H}` });
    const max = props.max ?? Math.max(1, ...vals);
    let d = '';
    let pen = false;
    props.values.forEach((v, i) => {
        if (v === null || v === undefined) {
            pen = false;
            return;
        }
        const px = (i / (props.values.length - 1)) * (W - 2) + 1;
        const py = H - 2 - (Math.min(v, max) / max) * (H - 4);
        d += `${pen ? 'L' : 'M'}${px.toFixed(1)},${py.toFixed(1)} `;
        pen = true;
    });
    return ((0, jsx_runtime_1.h)("svg", { className: "lv-spark", viewBox: `0 0 ${W} ${H}`, preserveAspectRatio: "none" },
        (0, jsx_runtime_1.h)("path", { d: d.trim(), fill: "none", stroke: props.color || '#38bdf8', strokeWidth: "1.5", strokeLinejoin: "round" })));
}
// Heat map: one row per GPU, one column per history sample. `idx` selects util(0) / vram(1) / temp(2) / power(3).
function Heatmap(props) {
    const cols = Math.min(props.maxCols || 120, props.points.length);
    if (cols < 2 || props.rows.length === 0) {
        return (0, jsx_runtime_1.h)("div", { className: "lv-chart-empty" }, "Collecting samples\u2026");
    }
    const pts = props.points.slice(-cols);
    const cw = 760 / cols;
    const rh = 18;
    const H = props.rows.length * rh + 4;
    const color = (v) => {
        if (v === undefined || v === null)
            return '#1b2436';
        const f = Math.max(0, Math.min(1, v / props.max));
        // blue → amber → red
        const hue = 210 - f * 210;
        return `hsl(${hue.toFixed(0)}, 70%, ${(22 + f * 28).toFixed(0)}%)`;
    };
    return ((0, jsx_runtime_1.h)("div", { className: "lv-heat-wrap" },
        (0, jsx_runtime_1.h)("div", { className: "lv-heat-labels", style: { height: `${H}px` } }, props.rows.map((r) => ((0, jsx_runtime_1.h)("div", { style: { height: `${rh}px` } }, r.label)))),
        (0, jsx_runtime_1.h)("svg", { className: "lv-heat", viewBox: `0 0 760 ${H}`, preserveAspectRatio: "none", style: { height: `${H}px` } }, props.rows.map((r, ri) => pts.map((p, ci) => {
            const v = p.g[r.id] ? p.g[r.id][props.idx] : undefined;
            return (0, jsx_runtime_1.h)("rect", { x: ci * cw, y: ri * rh + 2, width: cw + 0.4, height: rh - 2, fill: color(v) });
        })))));
}

  };
  __modules["live/views_a"] = function(module, exports, require) {
"use strict";
Object.defineProperty(exports, "__esModule", { value: true });
exports.verdictClass = verdictClass;
exports.renderCommandCenter = renderCommandCenter;
exports.stateKind = stateKind;
exports.renderPreflight = renderPreflight;
exports.renderGPUs = renderGPUs;
exports.renderHost = renderHost;
// Command center, preflight, GPU and host views of the live console.
const jsx_runtime_1 = require("../jsx-runtime");
const ui_1 = require("./ui");
const C = { blue: '#38bdf8', indigo: '#818cf8', green: '#10b981', amber: '#f59e0b', red: '#ef4444', purple: '#a855f7', slate: '#94a3b8' };
function sevKind(s) {
    return s === 'critical' ? 'critical' : s === 'warning' ? 'warning' : s === 'inconclusive' ? 'inconclusive' : 'healthy';
}
function verdictClass(v) {
    return v === 'ready' ? 'healthy' : v === 'degraded' ? 'warning' : v === 'blocked' ? 'critical' : 'inconclusive';
}
function renderCommandCenter(L, s, go, openSimulation) {
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
    const maxTemp = gpus.reduce((m, g) => {
        const t = g.latest.gpu_temp_celsius;
        return t === null || t === undefined ? m : m === null ? t : Math.max(m, t);
    }, null);
    const power = gpus.reduce((a, g) => a + (g.latest.power_usage_watts || 0), 0);
    const havePower = gpus.some((g) => g.latest.power_usage_watts !== null);
    const incidents = ov?.active_incidents || [];
    const welcome = gpus.length === 0 && s.status.tick > 0 && ((0, jsx_runtime_1.h)("div", { className: "panel lv-welcome" },
        (0, jsx_runtime_1.h)("div", { className: "panel-title" }, "Nothing to monitor on this machine yet"),
        (0, jsx_runtime_1.h)("p", { className: "lv-welcome-lead" },
            "GPU Lens checked every source and found no GPU. That is reported honestly as ",
            (0, jsx_runtime_1.h)("b", null, "no telemetry"),
            ", never as healthy. Pick a way forward:"),
        (0, jsx_runtime_1.h)("div", { className: "lv-welcome-grid" },
            (0, jsx_runtime_1.h)("div", { className: "lv-welcome-card" },
                (0, jsx_runtime_1.h)("b", null, "Look around first"),
                (0, jsx_runtime_1.h)("span", null, "Open the simulation: six deterministic cluster scenarios with full incident evidence. No GPU needed."),
                openSimulation && (0, jsx_runtime_1.h)("button", { className: "btn btn-primary", onClick: openSimulation }, "Open the simulation \u2192")),
            (0, jsx_runtime_1.h)("div", { className: "lv-welcome-card" },
                (0, jsx_runtime_1.h)("b", null, "Try Go Live without hardware"),
                (0, jsx_runtime_1.h)("span", null,
                    "Run ",
                    (0, jsx_runtime_1.h)("span", { className: "mono" }, "python scripts/live_lab.py"),
                    " next to GPU Lens: a clearly-labelled synthetic 8-GPU cluster appears here within seconds."),
                (0, jsx_runtime_1.h)("button", { className: "btn", onClick: () => go('preflight') }, "See what was checked")),
            (0, jsx_runtime_1.h)("div", { className: "lv-welcome-card" },
                (0, jsx_runtime_1.h)("b", null, "Connect real GPUs"),
                (0, jsx_runtime_1.h)("span", null, "Run GPU Lens on a machine with an NVIDIA driver, or point it at a DCGM exporter, Prometheus or Kubernetes API."),
                (0, jsx_runtime_1.h)("button", { className: "btn", onClick: () => go('connections') }, "Open Connections")))));
    return ((0, jsx_runtime_1.h)("div", null,
        welcome,
        (0, jsx_runtime_1.h)("div", { className: "lv-stat-grid" },
            (0, jsx_runtime_1.h)(ui_1.Stat, { label: "GPUs monitored", value: gpus.length, sub: `${fresh} fresh · ${gpus.length - fresh} stale`, kind: gpus.length - fresh > 0 ? 'warning' : '' }),
            (0, jsx_runtime_1.h)(ui_1.Stat, { label: "Fleet utilization", value: (0, ui_1.num)(latest?.util, 1, '%'), sub: "mean GPU SM util" }),
            (0, jsx_runtime_1.h)(ui_1.Stat, { label: "VRAM in use", value: (0, ui_1.num)(latest?.fb, 1, '%'), sub: "mean framebuffer" }),
            (0, jsx_runtime_1.h)(ui_1.Stat, { label: "Hottest GPU", value: (0, ui_1.num)(maxTemp, 0, '°C'), sub: `warn ${th.temp_warning_celsius}° · crit ${th.temp_critical_celsius}°`, kind: (0, ui_1.tone)(maxTemp, th.temp_warning_celsius, th.temp_critical_celsius) }),
            (0, jsx_runtime_1.h)(ui_1.Stat, { label: "GPU power draw", value: havePower ? (0, ui_1.num)(power, 0, ' W') : '—', sub: "sum of all GPUs" }),
            (0, jsx_runtime_1.h)(ui_1.Stat, { label: "Open incidents", value: counts.critical + counts.warning + counts.inconclusive, sub: `${counts.critical} critical · ${counts.warning} warning · ${counts.inconclusive} inconclusive`, kind: counts.critical ? 'critical' : counts.warning ? 'warning' : '' }),
            (0, jsx_runtime_1.h)(ui_1.Stat, { label: "Free allocatable", value: cap ? `${cap.total_free_allocatable_gpus}/${cap.total_allocatable_gpus}` : '—', sub: cap ? `${cap.pending_requested_gpus} requested by pending pods` : 'needs Kubernetes' }),
            (0, jsx_runtime_1.h)(ui_1.Stat, { label: "Sources", value: `${connected}/${enabled}`, sub: "connected / enabled", kind: connected < enabled ? 'warning' : '' })),
        (0, jsx_runtime_1.h)("div", { className: "lv-grid-2" },
            (0, jsx_runtime_1.h)("div", { className: "panel" },
                (0, jsx_runtime_1.h)("div", { className: "panel-header" },
                    (0, jsx_runtime_1.h)("div", { className: "panel-title" }, "Utilization & VRAM"),
                    (0, jsx_runtime_1.h)("span", { className: "lv-note" },
                        pts.length,
                        " samples this session")),
                (0, jsx_runtime_1.h)(ui_1.LineChart, { times: times, yMax: 100, series: [
                        { name: 'GPU util %', color: C.blue, values: pts.map((p) => p.util) },
                        { name: 'VRAM used %', color: C.indigo, values: pts.map((p) => p.fb) },
                    ] })),
            (0, jsx_runtime_1.h)("div", { className: "panel" },
                (0, jsx_runtime_1.h)("div", { className: "panel-header" },
                    (0, jsx_runtime_1.h)("div", { className: "panel-title" }, "Hottest GPU temperature"),
                    (0, jsx_runtime_1.h)("span", { className: "lv-note" }, "\u00B0C")),
                (0, jsx_runtime_1.h)(ui_1.LineChart, { times: times, yMin: 20, series: [{ name: 'max temp', color: C.amber, values: pts.map((p) => p.temp_max) }], threshold: { value: th.temp_critical_celsius, label: `critical ${th.temp_critical_celsius}°C` } })),
            (0, jsx_runtime_1.h)("div", { className: "panel" },
                (0, jsx_runtime_1.h)("div", { className: "panel-header" },
                    (0, jsx_runtime_1.h)("div", { className: "panel-title" }, "Total GPU power"),
                    (0, jsx_runtime_1.h)("span", { className: "lv-note" }, "watts")),
                (0, jsx_runtime_1.h)(ui_1.LineChart, { times: times, series: [{ name: 'power', color: C.green, values: pts.map((p) => p.power) }] })),
            (0, jsx_runtime_1.h)("div", { className: "panel" },
                (0, jsx_runtime_1.h)("div", { className: "panel-header" },
                    (0, jsx_runtime_1.h)("div", { className: "panel-title" }, "Open incidents over time"),
                    (0, jsx_runtime_1.h)("span", { className: "lv-note" }, "count")),
                (0, jsx_runtime_1.h)(ui_1.LineChart, { times: times, series: [
                        { name: 'critical', color: C.red, values: pts.map((p) => p.crit) },
                        { name: 'warning', color: C.amber, values: pts.map((p) => p.warn) },
                    ] }))),
        (0, jsx_runtime_1.h)("div", { className: "panel" },
            (0, jsx_runtime_1.h)("div", { className: "panel-header" },
                (0, jsx_runtime_1.h)("div", { className: "panel-title" }, "Per-GPU utilization heat map"),
                (0, jsx_runtime_1.h)("span", { className: "lv-note" }, "one row per GPU \u00B7 blue = idle \u2192 red = saturated")),
            (0, jsx_runtime_1.h)(ui_1.Heatmap, { rows: gpus.map((g) => ({ id: g.uuid, label: `${g.node_name.length > 18 ? '…' + g.node_name.slice(-17) : g.node_name}/gpu${g.device_index}` })), points: pts, idx: 0, max: 100 })),
        (0, jsx_runtime_1.h)("div", { className: "lv-grid-2" },
            (0, jsx_runtime_1.h)("div", { className: "panel" },
                (0, jsx_runtime_1.h)("div", { className: "panel-header" },
                    (0, jsx_runtime_1.h)("div", { className: "panel-title" }, "Active incidents"),
                    (0, jsx_runtime_1.h)("button", { className: "btn", onClick: () => go('incidents') }, "Open incident queue")),
                incidents.length === 0 ? ((0, jsx_runtime_1.h)(ui_1.Empty, { title: gpus.length === 0 ? 'No GPU telemetry yet' : 'No rule is firing' }, gpus.length === 0
                    ? 'Nothing can be called healthy until a source reports GPUs. See Check Everything.'
                    : 'All six deterministic rules evaluated this cycle with the thresholds shown in the Thresholds tab.')) : (incidents.slice(0, 6).map((inc) => ((0, jsx_runtime_1.h)("div", { className: "lv-row", key: inc.id },
                    (0, jsx_runtime_1.h)(ui_1.Badge, { kind: sevKind(inc.severity) }, inc.severity),
                    (0, jsx_runtime_1.h)("div", { style: { flex: '1' } },
                        (0, jsx_runtime_1.h)("div", { style: { fontWeight: 600 } }, inc.title),
                        (0, jsx_runtime_1.h)("div", { className: "lv-note mono" }, inc.rule_id))))))),
            (0, jsx_runtime_1.h)("div", { className: "panel" },
                (0, jsx_runtime_1.h)("div", { className: "panel-header" },
                    (0, jsx_runtime_1.h)("div", { className: "panel-title" }, "Sources at a glance"),
                    (0, jsx_runtime_1.h)("button", { className: "btn", onClick: () => go('sources') }, "Details")),
                s.sources.map((src) => ((0, jsx_runtime_1.h)("div", { className: "lv-row", key: src.id },
                    (0, jsx_runtime_1.h)("span", { className: `lv-dot ${src.state}` }),
                    (0, jsx_runtime_1.h)("div", { style: { flex: '1' } },
                        (0, jsx_runtime_1.h)("div", { style: { fontWeight: 600 } }, src.name),
                        (0, jsx_runtime_1.h)("div", { className: "lv-note" }, src.detail)),
                    (0, jsx_runtime_1.h)(ui_1.Badge, { kind: stateKind(src.state) }, ui_1.STATE_LABEL[src.state] || src.state))))))));
}
function stateKind(st) {
    switch (st) {
        case 'connected': return 'healthy';
        case 'degraded': return 'warning';
        case 'unreachable':
        case 'error': return 'critical';
        case 'not_found': return 'inconclusive';
        default: return 'info';
    }
}
function renderPreflight(L, s, recheck) {
    const pf = s.preflight;
    const sum = pf.summary || {};
    const now = Date.now();
    return ((0, jsx_runtime_1.h)("div", null,
        (0, jsx_runtime_1.h)("div", { className: "lv-stat-grid" },
            (0, jsx_runtime_1.h)(ui_1.Stat, { label: "Passed", value: sum.pass || 0, kind: "" }),
            (0, jsx_runtime_1.h)(ui_1.Stat, { label: "Warnings", value: sum.warn || 0, kind: sum.warn ? 'warning' : '' }),
            (0, jsx_runtime_1.h)(ui_1.Stat, { label: "Failed", value: sum.fail || 0, kind: sum.fail ? 'critical' : '' }),
            (0, jsx_runtime_1.h)(ui_1.Stat, { label: "Notes / skipped", value: `${sum.info || 0} / ${sum.skip || 0}`, sub: "skipped = needs a source that is not connected" })),
        (0, jsx_runtime_1.h)("div", { className: "lv-toolbar" },
            (0, jsx_runtime_1.h)("button", { className: "btn btn-primary", disabled: L.busy !== '', onClick: recheck }, L.busy === 'recheck' ? 'Checking…' : 'Re-check everything now'),
            (0, jsx_runtime_1.h)("span", { className: "lv-note" },
                "Evaluated ",
                (0, ui_1.ago)(pf.generated_at, now),
                " from live collector state. Checks marked \u201Cskipped\u201D need a source that is not connected.")),
        pf.groups.map((g) => ((0, jsx_runtime_1.h)("div", { className: "panel", key: g.id },
            (0, jsx_runtime_1.h)("div", { className: "panel-header" },
                (0, jsx_runtime_1.h)("div", { className: "panel-title" }, g.title),
                (0, jsx_runtime_1.h)("span", { className: "lv-note" },
                    g.checks.filter((c) => c.status === 'pass').length,
                    "/",
                    g.checks.length,
                    " passed")),
            g.checks.map((c) => ((0, jsx_runtime_1.h)("div", { className: `lv-check ${c.status}`, key: c.id },
                (0, jsx_runtime_1.h)("span", { className: `lv-check-icon ${c.status}` }, ui_1.CHECK_ICON[c.status] || '?'),
                (0, jsx_runtime_1.h)("div", { style: { flex: '1' } },
                    (0, jsx_runtime_1.h)("div", { style: { fontWeight: 600 } },
                        c.title,
                        c.latency_ms !== null && c.latency_ms !== undefined && (0, jsx_runtime_1.h)("span", { className: "lv-note mono" },
                            " \u00B7 ",
                            c.latency_ms.toFixed(0),
                            " ms")),
                    (0, jsx_runtime_1.h)("div", { className: "lv-note" }, c.detail),
                    c.hint && c.status !== 'pass' && (0, jsx_runtime_1.h)("div", { className: "lv-hint" },
                        "\u2192 ",
                        c.hint)),
                (0, jsx_runtime_1.h)("span", { className: "lv-check-status" }, c.status)))))))));
}
function gpuHist(L, uuid, idx) {
    return L.history.slice(-90).map((p) => (p.g[uuid] ? p.g[uuid][idx] : null));
}
function gpuMatches(g, f) {
    if (f.node !== 'all' && g.node_name !== f.node)
        return false;
    if (f.status === 'issues' && !g.worst_severity)
        return false;
    if (f.status === 'stale' && !g.is_stale)
        return false;
    if (f.status === 'idle' && !((g.latest.gpu_util_percent ?? 100) < 5))
        return false;
    if (f.q.trim()) {
        const q = f.q.toLowerCase();
        const hay = `${g.uuid} ${g.node_name} ${g.model_name} ${g.allocated_pod} ${g.allocated_namespace} gpu${g.device_index}`.toLowerCase();
        if (!hay.includes(q))
            return false;
    }
    return true;
}
const SEV_ORDER = { critical: 0, warning: 1, inconclusive: 2, info: 3, '': 4 };
function renderGPUs(L, s, f, setF, openUUID, setOpen) {
    const nodes = Array.from(new Set(s.gpus.map((g) => g.node_name))).sort();
    let list = s.gpus.filter((g) => gpuMatches(g, f));
    const val = (g) => {
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
        return ((0, jsx_runtime_1.h)("div", { className: "panel" },
            (0, jsx_runtime_1.h)(ui_1.Empty, { title: "No GPUs reported yet" },
                "GPU Lens has not received telemetry from any source. Open ",
                (0, jsx_runtime_1.h)("b", null, "Check Everything"),
                " to see what is missing, or start the Live Lab emulator (",
                (0, jsx_runtime_1.h)("span", { className: "mono" }, "python3 scripts/live_lab.py"),
                ") to try the console without hardware.")));
    }
    return ((0, jsx_runtime_1.h)("div", null,
        (0, jsx_runtime_1.h)("div", { className: "lv-toolbar" },
            (0, jsx_runtime_1.h)("input", { className: "lv-input", placeholder: "Search GPU, node, pod, UUID\u2026", value: f.q, onInput: (e) => setF({ ...f, q: e.target.value }) }),
            (0, jsx_runtime_1.h)("select", { className: "scenario-select", value: f.node, onChange: (e) => setF({ ...f, node: e.target.value }) },
                (0, jsx_runtime_1.h)("option", { value: "all", selected: f.node === 'all' }, "All nodes"),
                nodes.map((n) => (0, jsx_runtime_1.h)("option", { value: n, selected: f.node === n }, n))),
            (0, jsx_runtime_1.h)("select", { className: "scenario-select", value: f.status, onChange: (e) => setF({ ...f, status: e.target.value }) }, [['all', 'All states'], ['issues', 'With incidents'], ['stale', 'Stale telemetry'], ['idle', 'Idle (<5% util)']].map(([v, l]) => ((0, jsx_runtime_1.h)("option", { value: v, selected: f.status === v }, l)))),
            (0, jsx_runtime_1.h)("select", { className: "scenario-select", value: f.sort, onChange: (e) => setF({ ...f, sort: e.target.value }) }, [['index', 'Sort: node / index'], ['severity', 'Sort: severity'], ['util', 'Sort: utilization'], ['temp', 'Sort: temperature'], ['vram', 'Sort: VRAM'], ['power', 'Sort: power']].map(([v, l]) => ((0, jsx_runtime_1.h)("option", { value: v, selected: f.sort === v }, l)))),
            (0, jsx_runtime_1.h)("span", { className: "lv-note" },
                list.length,
                " of ",
                s.gpus.length,
                " GPUs")),
        (0, jsx_runtime_1.h)("div", { className: "lv-gpu-grid" }, list.map((g) => {
            const m = g.latest;
            const open = openUUID === g.uuid;
            const pctPower = m.power_usage_watts !== null && g.power_limit_watts > 0 ? (m.power_usage_watts / g.power_limit_watts) * 100 : null;
            return ((0, jsx_runtime_1.h)("div", { className: `lv-gpu ${g.worst_severity || ''} ${g.is_stale ? 'stale' : ''}`, key: g.uuid },
                (0, jsx_runtime_1.h)("div", { className: "lv-gpu-head" },
                    (0, jsx_runtime_1.h)("div", null,
                        (0, jsx_runtime_1.h)("div", { style: { fontWeight: 700 } },
                            g.node_name,
                            " \u00B7 GPU ",
                            g.device_index),
                        (0, jsx_runtime_1.h)("div", { className: "lv-note" },
                            g.model_name || 'Unknown model',
                            g.mig_profile ? ` · MIG ${g.mig_profile}` : '')),
                    (0, jsx_runtime_1.h)("div", { className: "lv-badges" },
                        g.worst_severity && (0, jsx_runtime_1.h)(ui_1.Badge, { kind: sevKind(g.worst_severity) }, g.worst_severity),
                        g.is_stale && (0, jsx_runtime_1.h)(ui_1.Badge, { kind: "inconclusive" },
                            "stale ",
                            (0, ui_1.duration)(g.staleness_seconds)),
                        g.emulated && (0, jsx_runtime_1.h)(ui_1.Badge, { kind: "info" }, "emulated"))),
                (0, jsx_runtime_1.h)("div", { className: "lv-workload" }, g.allocated_pod
                    ? (0, jsx_runtime_1.h)("span", null,
                        (0, jsx_runtime_1.h)("b", null,
                            g.allocated_namespace,
                            "/",
                            g.allocated_pod),
                        g.allocated_container ? ` · ${g.allocated_container}` : '')
                    : (0, jsx_runtime_1.h)("span", { className: "lv-note" }, g.mapping_status === 'mapped' ? 'No workload assigned' : `Workload mapping: ${g.mapping_status || 'unknown'}`)),
                (0, jsx_runtime_1.h)("div", { className: "lv-metric" },
                    (0, jsx_runtime_1.h)("span", null, "Utilization"),
                    (0, jsx_runtime_1.h)("b", null, (0, ui_1.num)(m.gpu_util_percent, 0, '%')),
                    (0, jsx_runtime_1.h)(ui_1.Meter, { value: m.gpu_util_percent }),
                    (0, jsx_runtime_1.h)(ui_1.Sparkline, { values: gpuHist(L, g.uuid, 0), max: 100, color: C.blue })),
                (0, jsx_runtime_1.h)("div", { className: "lv-metric" },
                    (0, jsx_runtime_1.h)("span", null, "VRAM"),
                    (0, jsx_runtime_1.h)("b", null, (0, ui_1.num)(g.fb_used_percent, 0, '%')),
                    (0, jsx_runtime_1.h)(ui_1.Meter, { value: g.fb_used_percent, warn: th.fb_memory_warning_percent, crit: 101 }),
                    (0, jsx_runtime_1.h)(ui_1.Sparkline, { values: gpuHist(L, g.uuid, 1), max: 100, color: C.indigo })),
                (0, jsx_runtime_1.h)("div", { className: "lv-metric" },
                    (0, jsx_runtime_1.h)("span", null, "Temperature"),
                    (0, jsx_runtime_1.h)("b", null, (0, ui_1.num)(m.gpu_temp_celsius, 0, '°C')),
                    (0, jsx_runtime_1.h)(ui_1.Meter, { value: m.gpu_temp_celsius, max: 100, warn: th.temp_warning_celsius, crit: th.temp_critical_celsius }),
                    (0, jsx_runtime_1.h)(ui_1.Sparkline, { values: gpuHist(L, g.uuid, 2), color: C.amber })),
                (0, jsx_runtime_1.h)("div", { className: "lv-metric" },
                    (0, jsx_runtime_1.h)("span", null, "Power"),
                    (0, jsx_runtime_1.h)("b", null, (0, ui_1.num)(m.power_usage_watts, 0, 'W')),
                    (0, jsx_runtime_1.h)(ui_1.Meter, { value: pctPower, warn: th.power_cap_warning_percent, crit: 101 }),
                    (0, jsx_runtime_1.h)(ui_1.Sparkline, { values: gpuHist(L, g.uuid, 3), color: C.green })),
                g.throttle_reasons && g.throttle_reasons.length > 0 && ((0, jsx_runtime_1.h)("div", { className: "lv-throttle" },
                    "Throttling: ",
                    g.throttle_reasons.join(', '))),
                (0, jsx_runtime_1.h)("div", { className: "lv-gpu-foot" },
                    (0, jsx_runtime_1.h)("span", { className: "lv-note mono" },
                        g.uuid.slice(0, 22),
                        "\u2026"),
                    (0, jsx_runtime_1.h)("button", { className: "btn", onClick: () => setOpen(open ? '' : g.uuid) }, open ? 'Hide details' : 'Details')),
                open && renderGPUDetail(g)));
        }))));
}
function renderGPUDetail(g) {
    const m = g.latest;
    const rows = [
        ['UUID', g.uuid], ['PCI bus', g.pci_bus_id || '—'], ['Driver', g.driver_version || '—'], ['P-state', g.pstate || '—'],
        ['Memory total', (0, ui_1.mib)(g.total_memory_mib || null)], ['VRAM used / free', `${(0, ui_1.mib)(m.fb_used_mib)} / ${(0, ui_1.mib)(m.fb_free_mib)}`],
        ['Memory copy util', (0, ui_1.num)(m.mem_copy_util_percent, 0, '%')], ['Memory (HBM) temp', (0, ui_1.num)(m.memory_temp_celsius, 0, '°C')],
        ['Power limit', g.power_limit_watts ? `${g.power_limit_watts} W` : '—'], ['Fan', g.fan_speed_percent === null ? '—' : `${g.fan_speed_percent}%`],
        ['PCIe link', g.pcie ? `Gen${g.pcie.gen_current}/${g.pcie.gen_max} · x${g.pcie.width_current}/${g.pcie.width_max}` : '—'],
        ['XID error', m.xid_error_code === null ? '— (not exported)' : m.xid_error_code], ['PCIe replays', m.pcie_replay_counter === null ? '— (not exported)' : m.pcie_replay_counter],
        ['ECC double-bit', m.ecc_dbe_vol_total === null ? '— (not exported)' : m.ecc_dbe_vol_total], ['ECC single-bit', m.ecc_sbe_vol_total === null ? '— (not exported)' : m.ecc_sbe_vol_total],
        ['Row-remap failure', m.row_remap_failure === null ? '— (not exported)' : m.row_remap_failure], ['Thermal violation', m.thermal_violation_ns === null ? '— (not exported)' : `${m.thermal_violation_ns} ns`],
        ['Reported by', (g.sources || []).join(', ') || '—'], ['Samples this session', g.sample_count], ['Incidents', (g.incident_ids || []).join(', ') || 'none'],
    ];
    return ((0, jsx_runtime_1.h)("div", { className: "lv-detail" },
        (0, jsx_runtime_1.h)("table", { className: "lv-kv" },
            (0, jsx_runtime_1.h)("tbody", null, rows.map(([k, v]) => (0, jsx_runtime_1.h)("tr", null,
                (0, jsx_runtime_1.h)("td", null, k),
                (0, jsx_runtime_1.h)("td", { className: "mono" }, v))))),
        g.processes && g.processes.length > 0 && ((0, jsx_runtime_1.h)("div", { style: { marginTop: '8px' } },
            (0, jsx_runtime_1.h)("div", { className: "lv-note", style: { marginBottom: '4px' } }, "Compute processes"),
            g.processes.map((p) => (0, jsx_runtime_1.h)("div", { className: "mono", style: { fontSize: '12px' } },
                "PID ",
                p.pid,
                " \u00B7 ",
                p.name,
                " \u00B7 ",
                (0, ui_1.mib)(p.used_memory_mib))))),
        (0, jsx_runtime_1.h)("div", { className: "lv-note", style: { marginTop: '6px' } }, "\u201C\u2014 (not exported)\u201D means the source does not publish that metric; GPU Lens never shows a missing metric as zero.")));
}
function renderHost(L, s) {
    const host = s.host;
    if (!host || !host.supported) {
        return ((0, jsx_runtime_1.h)("div", { className: "panel" },
            (0, jsx_runtime_1.h)(ui_1.Empty, { title: "Host vitals are not available" }, s.config.host_vitals_enabled
                ? 'This platform does not expose /proc and /sys (host vitals need Linux). GPU telemetry is unaffected.'
                : 'Host vitals are switched off in Connections.')));
    }
    const pts = L.history;
    const times = pts.map((p) => p.t);
    const cpus = host.logical_cpus || 1;
    return ((0, jsx_runtime_1.h)("div", null,
        (0, jsx_runtime_1.h)("div", { className: "lv-stat-grid" },
            (0, jsx_runtime_1.h)(ui_1.Stat, { label: "CPU", value: (0, ui_1.num)(host.cpu_percent, 1, '%'), sub: `${cpus} logical CPUs · iowait ${(0, ui_1.num)(host.cpu_iowait_percent, 1, '%')}`, kind: (0, ui_1.tone)(host.cpu_percent, 85, 95) }),
            (0, jsx_runtime_1.h)(ui_1.Stat, { label: "Load (1/5/15 min)", value: `${(0, ui_1.num)(host.load1, 2)} / ${(0, ui_1.num)(host.load5, 2)} / ${(0, ui_1.num)(host.load15, 2)}`, sub: host.load1 !== null && host.load1 > cpus ? 'above CPU count' : 'within CPU count', kind: host.load1 !== null && host.load1 > cpus ? 'warning' : '' }),
            (0, jsx_runtime_1.h)(ui_1.Stat, { label: "Memory", value: (0, ui_1.num)(host.mem_used_percent, 1, '%'), sub: `${(0, ui_1.mib)(host.mem_available_mib)} available of ${(0, ui_1.mib)(host.mem_total_mib)}`, kind: (0, ui_1.tone)(host.mem_used_percent, 85, 95) }),
            (0, jsx_runtime_1.h)(ui_1.Stat, { label: "Swap", value: host.swap_total_mib ? (0, ui_1.mib)(host.swap_used_mib) : 'none', sub: host.swap_total_mib ? `of ${(0, ui_1.mib)(host.swap_total_mib)}` : 'no swap configured' }),
            (0, jsx_runtime_1.h)(ui_1.Stat, { label: `Disk ${host.disk_path || ''}`, value: (0, ui_1.num)(host.disk_used_percent, 1, '%'), sub: host.disk_total_gib !== null ? `${(0, ui_1.num)(host.disk_total_gib, 0)} GiB total` : '', kind: (0, ui_1.tone)(host.disk_used_percent, 85, 95) }),
            (0, jsx_runtime_1.h)(ui_1.Stat, { label: "Network", value: `↓ ${(0, ui_1.bytesPerSec)(host.net_rx_bytes_per_sec)}`, sub: `↑ ${(0, ui_1.bytesPerSec)(host.net_tx_bytes_per_sec)}` }),
            (0, jsx_runtime_1.h)(ui_1.Stat, { label: "Uptime", value: (0, ui_1.duration)(host.uptime_seconds), sub: host.in_container ? 'inside a container' : 'bare metal / VM' }),
            (0, jsx_runtime_1.h)(ui_1.Stat, { label: "NVIDIA driver", value: host.nvidia_driver_version || 'not detected', sub: `${host.pci_gpus.length} NVIDIA PCI device(s)` })),
        (0, jsx_runtime_1.h)("div", { className: "lv-grid-2" },
            (0, jsx_runtime_1.h)("div", { className: "panel" },
                (0, jsx_runtime_1.h)("div", { className: "panel-header" },
                    (0, jsx_runtime_1.h)("div", { className: "panel-title" }, "Host CPU %")),
                (0, jsx_runtime_1.h)(ui_1.LineChart, { times: times, yMax: 100, series: [{ name: 'CPU', color: C.blue, values: pts.map((p) => p.cpu) }], height: 150 })),
            (0, jsx_runtime_1.h)("div", { className: "panel" },
                (0, jsx_runtime_1.h)("div", { className: "panel-header" },
                    (0, jsx_runtime_1.h)("div", { className: "panel-title" }, "Host memory %")),
                (0, jsx_runtime_1.h)(ui_1.LineChart, { times: times, yMax: 100, series: [{ name: 'Memory', color: C.purple, values: pts.map((p) => p.mem) }], height: 150 }))),
        (0, jsx_runtime_1.h)("div", { className: "lv-grid-2" },
            (0, jsx_runtime_1.h)("div", { className: "panel" },
                (0, jsx_runtime_1.h)("div", { className: "panel-header" },
                    (0, jsx_runtime_1.h)("div", { className: "panel-title" }, "System")),
                (0, jsx_runtime_1.h)("table", { className: "lv-kv" },
                    (0, jsx_runtime_1.h)("tbody", null, [
                        ['Hostname', host.hostname], ['OS', `${host.platform || host.os} (${host.arch})`], ['Kernel', host.kernel || '—'], ['CPU model', host.cpu_model || '—'],
                        ['Pressure (PSI, 10 s)', host.pressure ? `cpu ${(0, ui_1.num)(host.pressure.cpu_some_avg10, 2)} · mem ${(0, ui_1.num)(host.pressure.memory_some_avg10, 2)} · io ${(0, ui_1.num)(host.pressure.io_some_avg10, 2)}` : 'not available'],
                    ].map(([k, v]) => (0, jsx_runtime_1.h)("tr", null,
                        (0, jsx_runtime_1.h)("td", null, k),
                        (0, jsx_runtime_1.h)("td", { className: "mono" }, v)))))),
            (0, jsx_runtime_1.h)("div", { className: "panel" },
                (0, jsx_runtime_1.h)("div", { className: "panel-header" },
                    (0, jsx_runtime_1.h)("div", { className: "panel-title" }, "GPU Lens process")),
                (0, jsx_runtime_1.h)("table", { className: "lv-kv" },
                    (0, jsx_runtime_1.h)("tbody", null, [
                        ['PID', host.process.pid], ['Runtime', host.process.runtime], ['Uptime', (0, ui_1.duration)(host.process.uptime_seconds)],
                        ['Memory', (0, ui_1.mib)(host.process.memory_mib)], ['Workers', host.process.workers],
                    ].map(([k, v]) => (0, jsx_runtime_1.h)("tr", null,
                        (0, jsx_runtime_1.h)("td", null, k),
                        (0, jsx_runtime_1.h)("td", { className: "mono" }, v))))))),
        host.pci_gpus.length > 0 && ((0, jsx_runtime_1.h)("div", { className: "panel" },
            (0, jsx_runtime_1.h)("div", { className: "panel-header" },
                (0, jsx_runtime_1.h)("div", { className: "panel-title" }, "NVIDIA devices on the PCI bus")),
            (0, jsx_runtime_1.h)("table", { className: "data-table" },
                (0, jsx_runtime_1.h)("thead", null,
                    (0, jsx_runtime_1.h)("tr", null,
                        (0, jsx_runtime_1.h)("th", null, "Address"),
                        (0, jsx_runtime_1.h)("th", null, "Device ID"),
                        (0, jsx_runtime_1.h)("th", null, "Class"),
                        (0, jsx_runtime_1.h)("th", null, "Kernel driver"))),
                (0, jsx_runtime_1.h)("tbody", null, host.pci_gpus.map((d) => (0, jsx_runtime_1.h)("tr", null,
                    (0, jsx_runtime_1.h)("td", { className: "mono" }, d.address),
                    (0, jsx_runtime_1.h)("td", { className: "mono" },
                        d.vendor_id,
                        ":",
                        d.device_id),
                    (0, jsx_runtime_1.h)("td", null, d.class),
                    (0, jsx_runtime_1.h)("td", { className: "mono" }, d.driver || 'none bound'))))))),
        host.notes && host.notes.length > 0 && (0, jsx_runtime_1.h)("div", { className: "panel" }, host.notes.map((n) => (0, jsx_runtime_1.h)("div", { className: "lv-note" },
            "\u2022 ",
            n)))));
}

  };
  __modules["live/views_b"] = function(module, exports, require) {
"use strict";
Object.defineProperty(exports, "__esModule", { value: true });
exports.renderWorkloads = renderWorkloads;
exports.renderSources = renderSources;
exports.draftFromConfig = draftFromConfig;
exports.buildConfigPatch = buildConfigPatch;
exports.renderConnections = renderConnections;
exports.renderThresholds = renderThresholds;
exports.renderActivity = renderActivity;
// Workloads, sources, connections, thresholds, activity and incidents views of the live console.
const jsx_runtime_1 = require("../jsx-runtime");
const ui_1 = require("./ui");
const views_a_1 = require("./views_a");
function renderWorkloads(s) {
    const { nodes, pods, events, processes } = s;
    const k8s = s.sources.find((x) => x.id === 'kubernetes');
    const k8sUp = !!k8s && (k8s.state === 'connected' || k8s.state === 'degraded');
    return ((0, jsx_runtime_1.h)("div", null,
        !k8sUp && ((0, jsx_runtime_1.h)("div", { className: "blindspot-box" },
            (0, jsx_runtime_1.h)("strong", null, "No Kubernetes connection."),
            " Nodes and workloads below are inferred from GPU telemetry only (host processes), so scheduling, pending-pod and pod-attribution findings are unavailable. ",
            k8s?.hint)),
        (0, jsx_runtime_1.h)("div", { className: "panel" },
            (0, jsx_runtime_1.h)("div", { className: "panel-header" },
                (0, jsx_runtime_1.h)("div", { className: "panel-title" }, "Nodes"),
                (0, jsx_runtime_1.h)("span", { className: "lv-note" }, nodes.length)),
            nodes.length === 0 ? (0, jsx_runtime_1.h)(ui_1.Empty, { title: "No nodes" }, "No GPU nodes have been discovered yet.") : ((0, jsx_runtime_1.h)("table", { className: "data-table" },
                (0, jsx_runtime_1.h)("thead", null,
                    (0, jsx_runtime_1.h)("tr", null,
                        (0, jsx_runtime_1.h)("th", null, "Node"),
                        (0, jsx_runtime_1.h)("th", null, "Model"),
                        (0, jsx_runtime_1.h)("th", null, "Ready"),
                        (0, jsx_runtime_1.h)("th", null, "Schedulable"),
                        (0, jsx_runtime_1.h)("th", null, "GPUs (alloc / allocatable / capacity)"),
                        (0, jsx_runtime_1.h)("th", null, "Conditions"))),
                (0, jsx_runtime_1.h)("tbody", null, nodes.map((n) => ((0, jsx_runtime_1.h)("tr", { key: n.name },
                    (0, jsx_runtime_1.h)("td", { className: "mono" }, n.name),
                    (0, jsx_runtime_1.h)("td", null, n.gpu_model || '—'),
                    (0, jsx_runtime_1.h)("td", null,
                        (0, jsx_runtime_1.h)(ui_1.Badge, { kind: n.ready ? 'healthy' : 'critical' }, n.ready ? 'Ready' : 'NotReady')),
                    (0, jsx_runtime_1.h)("td", null, n.schedulable ? 'yes' : (0, jsx_runtime_1.h)(ui_1.Badge, { kind: "warning" }, "cordoned")),
                    (0, jsx_runtime_1.h)("td", { className: "mono" },
                        n.gpu_allocated,
                        " / ",
                        n.gpu_allocatable,
                        " / ",
                        n.gpu_capacity),
                    (0, jsx_runtime_1.h)("td", null, (n.conditions || []).filter((c) => c.status === 'True' && c.type !== 'Ready').map((c) => (0, jsx_runtime_1.h)(ui_1.Badge, { kind: "warning" }, c.type)))))))))),
        (0, jsx_runtime_1.h)("div", { className: "panel" },
            (0, jsx_runtime_1.h)("div", { className: "panel-header" },
                (0, jsx_runtime_1.h)("div", { className: "panel-title" }, "GPU workloads"),
                (0, jsx_runtime_1.h)("span", { className: "lv-note" }, pods.length)),
            pods.length === 0 ? (0, jsx_runtime_1.h)(ui_1.Empty, { title: "No GPU workloads" }, "No pod or host process is holding a GPU right now.") : ((0, jsx_runtime_1.h)("table", { className: "data-table" },
                (0, jsx_runtime_1.h)("thead", null,
                    (0, jsx_runtime_1.h)("tr", null,
                        (0, jsx_runtime_1.h)("th", null, "Workload"),
                        (0, jsx_runtime_1.h)("th", null, "Owner"),
                        (0, jsx_runtime_1.h)("th", null, "Phase"),
                        (0, jsx_runtime_1.h)("th", null, "Node"),
                        (0, jsx_runtime_1.h)("th", null, "GPUs"),
                        (0, jsx_runtime_1.h)("th", null, "Running for"),
                        (0, jsx_runtime_1.h)("th", null, "Restarts"),
                        (0, jsx_runtime_1.h)("th", null, "Detail"))),
                (0, jsx_runtime_1.h)("tbody", null, pods.map((p) => ((0, jsx_runtime_1.h)("tr", { key: p.uid || p.namespace + p.name },
                    (0, jsx_runtime_1.h)("td", { className: "mono" },
                        p.namespace,
                        "/",
                        p.name),
                    (0, jsx_runtime_1.h)("td", null, p.owner_kind ? `${p.owner_kind}/${p.owner_name}` : '—'),
                    (0, jsx_runtime_1.h)("td", null,
                        (0, jsx_runtime_1.h)(ui_1.Badge, { kind: p.phase === 'Running' ? 'healthy' : p.phase === 'Pending' ? 'warning' : p.phase === 'Failed' ? 'critical' : 'info' }, p.phase)),
                    (0, jsx_runtime_1.h)("td", { className: "mono" }, p.node_name || '—'),
                    (0, jsx_runtime_1.h)("td", null, p.requested_gpus),
                    (0, jsx_runtime_1.h)("td", null, p.started_at ? (0, ui_1.duration)((Date.now() - new Date(p.started_at).getTime()) / 1000) : '—'),
                    (0, jsx_runtime_1.h)("td", null, p.restart_count),
                    (0, jsx_runtime_1.h)("td", { className: "lv-note" }, p.scheduler_reason || p.status_detail || '')))))))),
        (0, jsx_runtime_1.h)("div", { className: "panel" },
            (0, jsx_runtime_1.h)("div", { className: "panel-header" },
                (0, jsx_runtime_1.h)("div", { className: "panel-title" }, "GPU compute processes (nvidia-smi)"),
                (0, jsx_runtime_1.h)("span", { className: "lv-note" }, processes.length)),
            processes.length === 0 ? (0, jsx_runtime_1.h)(ui_1.Empty, { title: "No process data" }, "Needs the local NVIDIA driver (nvidia-smi) on this machine; DCGM and Prometheus do not report processes.") : ((0, jsx_runtime_1.h)("table", { className: "data-table" },
                (0, jsx_runtime_1.h)("thead", null,
                    (0, jsx_runtime_1.h)("tr", null,
                        (0, jsx_runtime_1.h)("th", null, "PID"),
                        (0, jsx_runtime_1.h)("th", null, "Name"),
                        (0, jsx_runtime_1.h)("th", null, "Node"),
                        (0, jsx_runtime_1.h)("th", null, "GPU"),
                        (0, jsx_runtime_1.h)("th", null, "VRAM"))),
                (0, jsx_runtime_1.h)("tbody", null, processes.map((p) => (0, jsx_runtime_1.h)("tr", null,
                    (0, jsx_runtime_1.h)("td", { className: "mono" }, p.pid),
                    (0, jsx_runtime_1.h)("td", { className: "mono" }, p.name),
                    (0, jsx_runtime_1.h)("td", null, p.node_name),
                    (0, jsx_runtime_1.h)("td", null, p.gpu_index),
                    (0, jsx_runtime_1.h)("td", null, (0, ui_1.mib)(p.used_memory_mib)))))))),
        (0, jsx_runtime_1.h)("div", { className: "panel" },
            (0, jsx_runtime_1.h)("div", { className: "panel-header" },
                (0, jsx_runtime_1.h)("div", { className: "panel-title" }, "Kubernetes events"),
                (0, jsx_runtime_1.h)("span", { className: "lv-note" }, events.length)),
            events.length === 0 ? (0, jsx_runtime_1.h)(ui_1.Empty, { title: "No events" }, "No GPU-relevant Kubernetes events were returned.") : ((0, jsx_runtime_1.h)("table", { className: "data-table" },
                (0, jsx_runtime_1.h)("thead", null,
                    (0, jsx_runtime_1.h)("tr", null,
                        (0, jsx_runtime_1.h)("th", null, "Type"),
                        (0, jsx_runtime_1.h)("th", null, "Reason"),
                        (0, jsx_runtime_1.h)("th", null, "Object"),
                        (0, jsx_runtime_1.h)("th", null, "Message"),
                        (0, jsx_runtime_1.h)("th", null, "Count"),
                        (0, jsx_runtime_1.h)("th", null, "Last seen"))),
                (0, jsx_runtime_1.h)("tbody", null, events.slice(0, 100).map((e) => ((0, jsx_runtime_1.h)("tr", null,
                    (0, jsx_runtime_1.h)("td", null,
                        (0, jsx_runtime_1.h)(ui_1.Badge, { kind: e.type === 'Warning' ? 'warning' : 'info' }, e.type)),
                    (0, jsx_runtime_1.h)("td", null, e.reason),
                    (0, jsx_runtime_1.h)("td", { className: "mono" },
                        e.involved_object_kind,
                        "/",
                        e.involved_object_name),
                    (0, jsx_runtime_1.h)("td", null, e.message),
                    (0, jsx_runtime_1.h)("td", null, e.count),
                    (0, jsx_runtime_1.h)("td", { className: "mono" }, (0, ui_1.clock)(e.last_timestamp)))))))))));
}
function renderSources(s, recheck, busy) {
    const now = Date.now();
    const metrics = s.sources_detail?.exported_metrics || {};
    const metricNames = Object.keys(metrics).sort();
    return ((0, jsx_runtime_1.h)("div", null,
        (0, jsx_runtime_1.h)("div", { className: "lv-toolbar" },
            (0, jsx_runtime_1.h)("button", { className: "btn btn-primary", disabled: busy !== '', onClick: recheck }, "Probe every source now"),
            (0, jsx_runtime_1.h)("span", { className: "lv-note" }, "Every probe is read-only: GET/HEAD requests, or a fixed allow-list of nvidia-smi query flags.")),
        (0, jsx_runtime_1.h)("div", { className: "lv-source-grid" }, s.sources.map((src) => ((0, jsx_runtime_1.h)("div", { className: `lv-source ${src.state}`, key: src.id },
            (0, jsx_runtime_1.h)("div", { className: "lv-gpu-head" },
                (0, jsx_runtime_1.h)("div", null,
                    (0, jsx_runtime_1.h)("div", { style: { fontWeight: 700 } }, src.name),
                    (0, jsx_runtime_1.h)("div", { className: "lv-note mono", style: { wordBreak: 'break-all' } }, src.endpoint)),
                (0, jsx_runtime_1.h)("div", { className: "lv-badges" },
                    (0, jsx_runtime_1.h)(ui_1.Badge, { kind: (0, views_a_1.stateKind)(src.state) }, ui_1.STATE_LABEL[src.state] || src.state),
                    src.emulated && (0, jsx_runtime_1.h)(ui_1.Badge, { kind: "info" }, "emulated"),
                    src.explicit && (0, jsx_runtime_1.h)(ui_1.Badge, { kind: "healthy" }, "configured"))),
            (0, jsx_runtime_1.h)("div", { style: { margin: '8px 0' } }, src.detail),
            src.hint && src.state !== 'connected' && (0, jsx_runtime_1.h)("div", { className: "lv-hint" },
                "\u2192 ",
                src.hint),
            src.last_error && src.state !== 'connected' && (0, jsx_runtime_1.h)("div", { className: "lv-error mono" }, src.last_error),
            (0, jsx_runtime_1.h)("div", { className: "lv-source-stats" },
                (0, jsx_runtime_1.h)("div", null,
                    (0, jsx_runtime_1.h)("span", null, "Last success"),
                    (0, jsx_runtime_1.h)("b", null, (0, ui_1.ago)(src.last_success_at, now))),
                (0, jsx_runtime_1.h)("div", null,
                    (0, jsx_runtime_1.h)("span", null, "Latency"),
                    (0, jsx_runtime_1.h)("b", null, src.last_latency_ms === null ? '—' : `${src.last_latency_ms.toFixed(0)} ms`)),
                (0, jsx_runtime_1.h)("div", null,
                    (0, jsx_runtime_1.h)("span", null, "OK / failed"),
                    (0, jsx_runtime_1.h)("b", null,
                        src.success_count,
                        " / ",
                        src.failure_count)),
                (0, jsx_runtime_1.h)("div", null,
                    (0, jsx_runtime_1.h)("span", null, "Refresh"),
                    (0, jsx_runtime_1.h)("b", null, src.refresh_seconds ? `${src.refresh_seconds}s` : '—'))),
            src.latency_history_ms.length > 1 && (0, jsx_runtime_1.h)(ui_1.Sparkline, { values: src.latency_history_ms, color: "#38bdf8", width: 300, height: 30 }),
            src.facts.length > 0 && ((0, jsx_runtime_1.h)("table", { className: "lv-kv", style: { marginTop: '6px' } },
                (0, jsx_runtime_1.h)("tbody", null, src.facts.map((f) => (0, jsx_runtime_1.h)("tr", null,
                    (0, jsx_runtime_1.h)("td", null, f.label),
                    (0, jsx_runtime_1.h)("td", { className: "mono" }, f.value)))))),
            (0, jsx_runtime_1.h)("div", { className: "lv-note", style: { marginTop: '6px' } },
                "\uD83D\uDD12 ",
                src.read_only))))),
        (0, jsx_runtime_1.h)("div", { className: "panel" },
            (0, jsx_runtime_1.h)("div", { className: "panel-header" },
                (0, jsx_runtime_1.h)("div", { className: "panel-title" }, "Metric coverage"),
                (0, jsx_runtime_1.h)("span", { className: "lv-note" }, "which GPU signals the connected sources export")),
            metricNames.length === 0 ? (0, jsx_runtime_1.h)(ui_1.Empty, { title: "No metric inventory yet" }, "A DCGM exporter or Prometheus must be connected to list metric coverage.") : ((0, jsx_runtime_1.h)("div", { className: "lv-chips" }, metricNames.map((m) => (0, jsx_runtime_1.h)("span", { className: `lv-chip ${metrics[m] ? 'on' : 'off'}` },
                metrics[m] ? '✓' : '✕',
                " ",
                m)))),
            (0, jsx_runtime_1.h)("div", { className: "lv-note", style: { marginTop: '8px' } }, "A metric that is not exported is reported as \u201Cnot exported\u201D \u2014 never as 0 and never as healthy.")),
        s.notes && s.notes.length > 0 && (0, jsx_runtime_1.h)("div", { className: "panel" },
            (0, jsx_runtime_1.h)("div", { className: "panel-title" }, "Notes"),
            s.notes.map((n) => (0, jsx_runtime_1.h)("div", { className: "lv-note" },
                "\u2022 ",
                n)))));
}
function draftFromConfig(c) {
    const src = (x) => ({ enabled: x.enabled, endpoint: x.explicit ? x.endpoint : '', token: '', clear: false });
    return {
        cluster_name: c.cluster_name || '',
        interval: String(c.interval_seconds),
        nvidia: c.nvidia_smi_enabled,
        host: c.host_vitals_enabled,
        dcgm: src(c.dcgm_exporter), prom: src(c.prometheus), k8s: src(c.kubernetes),
    };
}
function patchFor(d, cur) {
    const p = { enabled: d.enabled, endpoint: d.endpoint.trim() };
    if (d.token.trim())
        p.bearer_token = d.token.trim();
    if (d.clear && cur.token_set)
        p.clear_token = true;
    return p;
}
function buildConfigPatch(d, c) {
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
function srcEditor(title, help, placeholder, d, cur, set, locked) {
    return ((0, jsx_runtime_1.h)("div", { className: "lv-conn" },
        (0, jsx_runtime_1.h)("div", { className: "lv-conn-head" },
            (0, jsx_runtime_1.h)("label", { className: "lv-check-label" },
                (0, jsx_runtime_1.h)("input", { type: "checkbox", checked: d.enabled, disabled: locked, onChange: (e) => set({ ...d, enabled: e.target.checked }) }),
                (0, jsx_runtime_1.h)("b", null, title)),
            (0, jsx_runtime_1.h)("span", { className: "lv-note" }, cur.explicit ? 'endpoint set explicitly' : 'auto-probing the default address')),
        (0, jsx_runtime_1.h)("div", { className: "lv-note", style: { marginBottom: '6px' } }, help),
        (0, jsx_runtime_1.h)("input", { className: "lv-input wide mono", placeholder: placeholder, value: d.endpoint, disabled: locked || !d.enabled, onInput: (e) => set({ ...d, endpoint: e.target.value }) }),
        (0, jsx_runtime_1.h)("input", { className: "lv-input wide mono", type: "password", autocomplete: "off", disabled: locked || !d.enabled, placeholder: cur.token_set ? 'Bearer token is set — leave blank to keep it' : 'Bearer token (optional)', value: d.token, onInput: (e) => set({ ...d, token: e.target.value }) }),
        cur.token_set && ((0, jsx_runtime_1.h)("label", { className: "lv-check-label lv-note" },
            (0, jsx_runtime_1.h)("input", { type: "checkbox", checked: d.clear, disabled: locked, onChange: (e) => set({ ...d, clear: e.target.checked }) }),
            " remove the stored token")),
        cur.token_file && (0, jsx_runtime_1.h)("div", { className: "lv-note" }, "Token is read from a file configured at startup.")));
}
function renderConnections(s, d, setD, save, reload, busy) {
    const locked = !s.status.config_mutable;
    return ((0, jsx_runtime_1.h)("div", null,
        locked && ((0, jsx_runtime_1.h)("div", { className: "blindspot-box" },
            (0, jsx_runtime_1.h)("strong", null, "Connection settings are read-only here."),
            " ",
            s.status.config_mutable_reason || 'Changing endpoints and tokens is disabled for this session.')),
        (0, jsx_runtime_1.h)("div", { className: "panel" },
            (0, jsx_runtime_1.h)("div", { className: "panel-header" },
                (0, jsx_runtime_1.h)("div", { className: "panel-title" }, "Telemetry sources"),
                (0, jsx_runtime_1.h)("span", { className: "lv-note" }, "Saving restarts the live session and re-checks every source from scratch.")),
            (0, jsx_runtime_1.h)("div", { className: "lv-conn-grid" },
                srcEditor('DCGM exporter', 'Direct scrape of dcgm-exporter /metrics — the lowest-latency GPU source.', 'http://gpu-node:9400/metrics   (blank = probe localhost:9400)', d.dcgm, s.config.dcgm_exporter, (x) => setD({ ...d, dcgm: x }), locked),
                srcEditor('Prometheus', 'Read-only query API. Adds history backfill and cluster-wide coverage.', 'http://prometheus:9090   (blank = probe localhost:9090)', d.prom, s.config.prometheus, (x) => setD({ ...d, prom: x }), locked),
                srcEditor('Kubernetes API', 'Pods, nodes and events (GET/list/watch only). In a cluster the service account is used automatically; otherwise run kubectl proxy.', 'http://127.0.0.1:8001   (blank = probe kubectl proxy)', d.k8s, s.config.kubernetes, (x) => setD({ ...d, k8s: x }), locked))),
        (0, jsx_runtime_1.h)("div", { className: "panel" },
            (0, jsx_runtime_1.h)("div", { className: "panel-header" },
                (0, jsx_runtime_1.h)("div", { className: "panel-title" }, "This machine & session")),
            (0, jsx_runtime_1.h)("div", { className: "lv-form-grid" },
                (0, jsx_runtime_1.h)("label", { className: "lv-check-label" },
                    (0, jsx_runtime_1.h)("input", { type: "checkbox", checked: d.nvidia, disabled: locked, onChange: (e) => setD({ ...d, nvidia: e.target.checked }) }),
                    " Query the local NVIDIA driver (nvidia-smi)"),
                (0, jsx_runtime_1.h)("label", { className: "lv-check-label" },
                    (0, jsx_runtime_1.h)("input", { type: "checkbox", checked: d.host, disabled: locked, onChange: (e) => setD({ ...d, host: e.target.checked }) }),
                    " Read host vitals (CPU, memory, disk, network)"),
                (0, jsx_runtime_1.h)("label", null,
                    "Cluster / host label",
                    (0, jsx_runtime_1.h)("input", { className: "lv-input wide", value: d.cluster_name, disabled: locked, placeholder: `live@${s.status.hostname}`, onInput: (e) => setD({ ...d, cluster_name: e.target.value }) })),
                (0, jsx_runtime_1.h)("label", null,
                    "Refresh interval (seconds)",
                    (0, jsx_runtime_1.h)("select", { className: "scenario-select", disabled: locked, value: d.interval, onChange: (e) => setD({ ...d, interval: e.target.value }) }, ['1', '2', '5', '10', '15', '30', '60'].map((v) => (0, jsx_runtime_1.h)("option", { value: v, selected: d.interval === v },
                        v,
                        "s"))))),
            (0, jsx_runtime_1.h)("div", { className: "lv-toolbar", style: { marginTop: '12px' } },
                (0, jsx_runtime_1.h)("button", { className: "btn btn-primary", disabled: locked || busy !== '', onClick: save }, busy === 'config' ? 'Saving…' : 'Save & re-check everything'),
                (0, jsx_runtime_1.h)("button", { className: "btn", disabled: busy !== '', onClick: reload }, "Discard changes"),
                (0, jsx_runtime_1.h)("span", { className: "lv-note" }, "Tokens are write-only: they are never sent back to the browser."))),
        (0, jsx_runtime_1.h)("div", { className: "panel" },
            (0, jsx_runtime_1.h)("div", { className: "panel-header" },
                (0, jsx_runtime_1.h)("div", { className: "panel-title" }, "Startup-only settings")),
            (0, jsx_runtime_1.h)("table", { className: "lv-kv" },
                (0, jsx_runtime_1.h)("tbody", null, [
                    ['nvidia-smi path', s.config.nvidia_smi_path || '(auto-discovered)'], ['History kept', `${s.config.history_points} samples`],
                    ['Idle auto-stop', s.config.idle_timeout_seconds ? (0, ui_1.duration)(s.config.idle_timeout_seconds) : 'never'],
                    ['Prometheus backfill', `${s.config.prometheus_backfill_minutes} min`], ['Running in a cluster', s.config.in_cluster ? 'yes (service account)' : 'no'],
                    ['Remote config changes', s.config.allow_remote_config ? 'allowed' : 'local machine only'],
                ].map(([k, v]) => (0, jsx_runtime_1.h)("tr", null,
                    (0, jsx_runtime_1.h)("td", null, k),
                    (0, jsx_runtime_1.h)("td", { className: "mono" }, v))))),
            (0, jsx_runtime_1.h)("div", { className: "lv-note", style: { marginTop: '6px' } }, "Binary paths, token files and CA files can only be set with flags or GPULENS_* environment variables, never from the browser."))));
}
// ---------- Thresholds ----------
const TH_LABELS = {
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
function renderThresholds(s, draft, setDraft, apply, reset, busy) {
    const keys = Object.keys(s.thresholds).sort((a, b) => (TH_LABELS[a]?.[0] || a).localeCompare(TH_LABELS[b]?.[0] || b));
    const changed = keys.filter((k) => draft[k] !== undefined && draft[k] !== '' && Number(draft[k]) !== s.thresholds[k]);
    return ((0, jsx_runtime_1.h)("div", { className: "panel" },
        (0, jsx_runtime_1.h)("div", { className: "panel-header" },
            (0, jsx_runtime_1.h)("div", { className: "panel-title" }, "Rule thresholds for this live session"),
            (0, jsx_runtime_1.h)("span", { className: "lv-note" }, "The simulated demo always uses the defaults; changes here only affect Go Live.")),
        (0, jsx_runtime_1.h)("table", { className: "data-table" },
            (0, jsx_runtime_1.h)("thead", null,
                (0, jsx_runtime_1.h)("tr", null,
                    (0, jsx_runtime_1.h)("th", null, "Threshold"),
                    (0, jsx_runtime_1.h)("th", null, "Default"),
                    (0, jsx_runtime_1.h)("th", null, "Active"),
                    (0, jsx_runtime_1.h)("th", null, "New value"))),
            (0, jsx_runtime_1.h)("tbody", null, keys.map((k) => {
                const over = s.threshold_overrides.indexOf(k) >= 0;
                return ((0, jsx_runtime_1.h)("tr", { key: k },
                    (0, jsx_runtime_1.h)("td", null,
                        (0, jsx_runtime_1.h)("div", null, TH_LABELS[k]?.[0] || k),
                        (0, jsx_runtime_1.h)("div", { className: "lv-note mono" }, k)),
                    (0, jsx_runtime_1.h)("td", { className: "mono" }, s.threshold_defaults[k]),
                    (0, jsx_runtime_1.h)("td", { className: "mono" },
                        s.thresholds[k],
                        " ",
                        over && (0, jsx_runtime_1.h)(ui_1.Badge, { kind: "warning" }, "overridden")),
                    (0, jsx_runtime_1.h)("td", null,
                        (0, jsx_runtime_1.h)("input", { className: "lv-input mono", style: { width: '110px' }, type: "number", step: "any", placeholder: String(s.thresholds[k]), value: draft[k] ?? '', onInput: (e) => setDraft({ ...draft, [k]: e.target.value }) }),
                        " ",
                        (0, jsx_runtime_1.h)("span", { className: "lv-note" }, TH_LABELS[k]?.[1] || ''))));
            }))),
        (0, jsx_runtime_1.h)("div", { className: "lv-toolbar", style: { marginTop: '12px' } },
            (0, jsx_runtime_1.h)("button", { className: "btn btn-primary", disabled: changed.length === 0 || busy !== '', onClick: apply },
                "Apply ",
                changed.length || '',
                " change",
                changed.length === 1 ? '' : 's'),
            (0, jsx_runtime_1.h)("button", { className: "btn", disabled: s.threshold_overrides.length === 0 || busy !== '', onClick: reset }, "Reset all to defaults"),
            (0, jsx_runtime_1.h)("span", { className: "lv-note" }, "Invalid combinations (for example warning above critical) are rejected by the server."))));
}
// ---------- Activity ----------
function renderActivity(L, s, filter, setFilter) {
    const rows = L.activity.slice().reverse().filter((a) => filter === 'all' || a.severity === filter || a.kind === filter);
    const resolved = s.resolved_incidents || [];
    return ((0, jsx_runtime_1.h)("div", null,
        (0, jsx_runtime_1.h)("div", { className: "panel" },
            (0, jsx_runtime_1.h)("div", { className: "panel-header" },
                (0, jsx_runtime_1.h)("div", { className: "panel-title" }, "Activity log"),
                (0, jsx_runtime_1.h)("select", { className: "scenario-select", value: filter, onChange: (e) => setFilter(e.target.value) }, [['all', 'Everything'], ['critical', 'Critical'], ['warning', 'Warnings'], ['success', 'Recoveries'], ['source', 'Source changes'], ['incident', 'Incidents']].map(([v, l]) => ((0, jsx_runtime_1.h)("option", { value: v, selected: filter === v }, l))))),
            rows.length === 0 ? (0, jsx_runtime_1.h)(ui_1.Empty, { title: "Nothing logged yet" }, "Connection changes, incidents opening and resolving, and stale-telemetry events appear here as they happen.") : (rows.slice(0, 300).map((a) => ((0, jsx_runtime_1.h)("div", { className: "lv-row", key: a.seq },
                (0, jsx_runtime_1.h)("span", { className: "mono lv-note", style: { minWidth: '64px' } }, (0, ui_1.clock)(a.t)),
                (0, jsx_runtime_1.h)(ui_1.Badge, { kind: a.severity === 'critical' ? 'critical' : a.severity === 'warning' ? 'warning' : a.severity === 'success' ? 'healthy' : 'info' }, a.kind),
                (0, jsx_runtime_1.h)("div", { style: { flex: '1' } },
                    (0, jsx_runtime_1.h)("div", { style: { fontWeight: 600 } }, a.title),
                    a.detail && (0, jsx_runtime_1.h)("div", { className: "lv-note" }, a.detail))))))),
        (0, jsx_runtime_1.h)("div", { className: "panel" },
            (0, jsx_runtime_1.h)("div", { className: "panel-header" },
                (0, jsx_runtime_1.h)("div", { className: "panel-title" }, "Resolved this session"),
                (0, jsx_runtime_1.h)("span", { className: "lv-note" }, resolved.length)),
            resolved.length === 0 ? (0, jsx_runtime_1.h)(ui_1.Empty, { title: "No incident has resolved yet" }, "Incidents that stop firing are listed here with how long they lasted.") : (resolved.slice().reverse().map((r) => ((0, jsx_runtime_1.h)("div", { className: "lv-row", key: r.incident.id + r.resolved_at },
                (0, jsx_runtime_1.h)(ui_1.Badge, { kind: "healthy" }, "resolved"),
                (0, jsx_runtime_1.h)("div", { style: { flex: '1' } },
                    (0, jsx_runtime_1.h)("div", { style: { fontWeight: 600 } }, r.incident.title),
                    (0, jsx_runtime_1.h)("div", { className: "lv-note" },
                        r.incident.rule_id,
                        " \u00B7 lasted ",
                        (0, ui_1.duration)(r.duration_seconds),
                        " \u00B7 resolved at ",
                        (0, ui_1.clock)(r.resolved_at))))))))));
}

  };
  __modules["main"] = function(module, exports, require) {
"use strict";
Object.defineProperty(exports, "__esModule", { value: true });
const jsx_runtime_1 = require("./jsx-runtime");
const App_1 = require("./App");
const LiveConsole_1 = require("./live/LiveConsole");
const mode_1 = require("./mode");
const theme_1 = require("./theme");
const rootEl = document.getElementById('root');
if (rootEl) {
    (0, theme_1.initTheme)();
    (0, mode_1.initMode)();
    (0, mode_1.mountLauncher)();
    (0, theme_1.mountThemeToggle)();
    (0, jsx_runtime_1.mountApp)(() => ((0, mode_1.isLive)() ? (0, jsx_runtime_1.h)(LiveConsole_1.LiveConsole, { onExit: () => (0, mode_1.setLive)(false) }) : (0, jsx_runtime_1.h)(App_1.App, null)), rootEl);
}

  };
  __modules["mode"] = function(module, exports, require) {
"use strict";
Object.defineProperty(exports, "__esModule", { value: true });
exports.isLive = isLive;
exports.setLive = setLive;
exports.initMode = initMode;
exports.mountLauncher = mountLauncher;
// Switches between Go Live (the default view) and the simulated demo (?view=demo). The simulation's DOM is never
// modified: the launcher button is a separate element outside the app root.
const jsx_runtime_1 = require("./jsx-runtime");
let liveMode = true;
function isLive() {
    return liveMode;
}
function syncUrl() {
    try {
        const u = new URL(window.location.href);
        if (liveMode)
            u.searchParams.delete('view');
        else {
            u.searchParams.set('view', 'demo');
            u.searchParams.delete('live_tab');
        }
        window.history.replaceState(null, '', u.toString());
    }
    catch {
        /* ignore */
    }
    const btn = document.getElementById('golive-launcher');
    if (btn)
        btn.style.display = liveMode ? 'none' : '';
}
function setLive(on) {
    if (liveMode === on)
        return;
    liveMode = on;
    syncUrl();
    window.scrollTo(0, 0);
    (0, jsx_runtime_1.requestRender)();
}
function initMode() {
    try {
        // Go Live opens first; ?view=demo opens the simulation. (Old ?view=live links keep working.)
        liveMode = new URLSearchParams(window.location.search).get('view') !== 'demo';
    }
    catch {
        liveMode = true;
    }
}
/** The "Go Live" launcher: plain DOM appended to <body>, outside the demo's #root. */
function mountLauncher() {
    if (document.getElementById('golive-launcher'))
        return;
    const btn = document.createElement('button');
    btn.id = 'golive-launcher';
    btn.type = 'button';
    btn.className = 'golive-launcher';
    btn.title = 'Switch from the simulation to live telemetry from this machine and your GPU infrastructure';
    btn.innerHTML = '<span class="golive-dot"></span><span class="golive-text">Go Live</span>';
    btn.addEventListener('click', () => setLive(true));
    document.body.appendChild(btn);
    btn.style.display = liveMode ? 'none' : '';
}

  };
  __modules["theme"] = function(module, exports, require) {
"use strict";
Object.defineProperty(exports, "__esModule", { value: true });
exports.applyTheme = applyTheme;
exports.currentTheme = currentTheme;
exports.initTheme = initTheme;
exports.mountThemeToggle = mountThemeToggle;
const KEY = 'gpulens-theme';
function read() {
    try {
        const v = window.localStorage.getItem(KEY);
        if (v === 'dark' || v === 'light')
            return v;
    }
    catch {
        /* storage can be unavailable; fall through */
    }
    return 'light';
}
function applyTheme(t) {
    document.documentElement.setAttribute('data-theme', t);
    const btn = document.getElementById('theme-toggle');
    if (btn) {
        btn.textContent = t === 'light' ? '☾ Dark mode' : '☀ Light mode';
        btn.setAttribute('aria-label', t === 'light' ? 'Switch to dark mode' : 'Switch to light mode');
    }
}
function currentTheme() {
    return document.documentElement.getAttribute('data-theme') === 'dark' ? 'dark' : 'light';
}
function initTheme() {
    applyTheme(read());
}
/** A small fixed button, plain DOM outside the app root. */
function mountThemeToggle() {
    if (document.getElementById('theme-toggle'))
        return;
    const btn = document.createElement('button');
    btn.id = 'theme-toggle';
    btn.type = 'button';
    btn.className = 'theme-toggle';
    btn.addEventListener('click', () => {
        const next = currentTheme() === 'light' ? 'dark' : 'light';
        try {
            window.localStorage.setItem(KEY, next);
        }
        catch {
            /* ignore */
        }
        applyTheme(next);
    });
    document.body.appendChild(btn);
    applyTheme(currentTheme());
}

  };
  __modules["types"] = function(module, exports, require) {
"use strict";
// Strict TypeScript interfaces matching the Go backend REST API responses (`internal/domain/models.go`).
Object.defineProperty(exports, "__esModule", { value: true });

  };
  const rt = __require("jsx-runtime");
  window.h = rt.h;
  window.Fragment = rt.Fragment;
  __require("main");
})();
