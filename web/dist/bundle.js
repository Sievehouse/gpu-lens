(function() {
  const __modules = {};
  const __cache = {};
  function __require(name) {
    if (__cache[name]) return __cache[name].exports;
    const fn = __modules[name];
    if (!fn) throw new Error("Module not found: " + name);
    const mod = { exports: {} };
    __cache[name] = mod;
    fn(mod, mod.exports, __require);
    return mod.exports;
  }
  __modules["./types"] = function(module, exports, require) {
"use strict";
  };
  __modules["./jsx-runtime"] = function(module, exports, require) {
"use strict";

Object.defineProperty(exports, "__esModule", {
  value: true
});
exports.Fragment = void 0;
exports.h = h;
exports.mountApp = mountApp;
exports.useEffect = useEffect;
exports.useState = useState;
// Lightweight, deterministic JSX + Hooks runtime for offline compilation in air-gapped environments.
// Allows authoring standard TypeScript + TSX components (`App.tsx`) with `useState` and `useEffect`.

const Fragment = '__FRAGMENT__';
exports.Fragment = Fragment;
function h(tag, props, ...children) {
  return {
    tag,
    props: props || {},
    children
  };
}
let hookStates = [];
let hookDeps = [];
let hookIndex = 0;
let rootFactory = null;
let rootContainer = null;
let renderScheduled = false;
function useState(initial) {
  const idx = hookIndex++;
  if (hookStates.length <= idx) {
    hookStates[idx] = initial;
  }
  const setState = next => {
    const prev = hookStates[idx];
    const resolved = typeof next === 'function' ? next(prev) : next;
    if (resolved !== prev) {
      hookStates[idx] = resolved;
      scheduleRender();
    }
  };
  return [hookStates[idx], setState];
}
function useEffect(effect, deps) {
  const idx = hookIndex++;
  const prevDeps = hookDeps[idx];
  let changed = true;
  if (deps && prevDeps && deps.length === prevDeps.length) {
    changed = deps.some((d, i) => d !== prevDeps[i]);
  }
  if (changed) {
    hookDeps[idx] = deps;
    setTimeout(() => {
      effect();
    }, 0);
  }
}
function scheduleRender() {
  if (renderScheduled) return;
  renderScheduled = true;
  Promise.resolve().then(() => {
    renderScheduled = false;
    performRender();
  });
}
const SVG_NS = 'http://www.w3.org/2000/svg';
const SVG_TAGS = new Set(['svg', 'path', 'line', 'rect', 'circle', 'g', 'text', 'polyline', 'polygon', 'defs', 'linearGradient', 'stop']);
const SVG_ATTR_MAP = {
  viewBox: 'viewBox',
  strokeWidth: 'stroke-width',
  strokeDasharray: 'stroke-dasharray',
  textAnchor: 'text-anchor',
  fontSize: 'font-size',
  fontWeight: 'font-weight',
  fontFamily: 'font-family'
};
function createDOMNode(node, isSvg = false) {
  if (node === null || node === undefined || node === false || node === true) {
    return document.createTextNode('');
  }
  if (typeof node === 'string' || typeof node === 'number') {
    return document.createTextNode(String(node));
  }
  if (Array.isArray(node)) {
    const frag = document.createDocumentFragment();
    for (const child of node) {
      frag.appendChild(createDOMNode(child, isSvg));
    }
    return frag;
  }
  if (node.tag === Fragment) {
    const frag = document.createDocumentFragment();
    for (const child of node.children) {
      frag.appendChild(createDOMNode(child, isSvg));
    }
    return frag;
  }
  if (typeof node.tag === 'function') {
    const rendered = node.tag({
      ...node.props,
      children: node.children
    });
    return createDOMNode(rendered, isSvg);
  }
  const tagStr = String(node.tag);
  const useSvg = isSvg || SVG_TAGS.has(tagStr);
  const el = useSvg ? document.createElementNS(SVG_NS, tagStr) : document.createElement(tagStr);
  for (const [k, v] of Object.entries(node.props || {})) {
    if (v === undefined || v === null || v === false) continue;
    if (k === 'className') {
      el.setAttribute('class', String(v));
    } else if (k === 'style' && typeof v === 'object') {
      Object.assign(el.style, v);
    } else if (k.startsWith('on') && typeof v === 'function') {
      const evName = k.slice(2).toLowerCase();
      el.addEventListener(evName, v);
    } else if (!useSvg && k === 'value' && 'value' in el) {
      el.value = String(v);
      el.setAttribute('value', String(v));
    } else if (!useSvg && k === 'selected' && v === true) {
      el.selected = true;
      el.setAttribute('selected', 'selected');
    } else {
      const attrName = useSvg && SVG_ATTR_MAP[k] ? SVG_ATTR_MAP[k] : k;
      el.setAttribute(attrName, String(v));
    }
  }
  for (const child of node.children) {
    el.appendChild(createDOMNode(child, useSvg));
  }
  if (tagStr === 'select' && node.props && node.props.value !== undefined) {
    el.value = String(node.props.value);
  }
  return el;
}
function performRender() {
  if (!rootContainer || !rootFactory) return;
  hookIndex = 0;
  const vtree = rootFactory();
  rootContainer.innerHTML = '';
  rootContainer.appendChild(createDOMNode(vtree, false));
}
function mountApp(factory, container) {
  rootFactory = factory;
  rootContainer = container;
  performRender();
}
  };
  __modules["./api"] = function(module, exports, require) {
"use strict";

Object.defineProperty(exports, "__esModule", {
  value: true
});
exports.fetchCapacity = fetchCapacity;
exports.fetchIncidentDetail = fetchIncidentDetail;
exports.fetchIncidents = fetchIncidents;
exports.fetchOverview = fetchOverview;
exports.fetchRules = fetchRules;
exports.fetchScenarios = fetchScenarios;
exports.fetchSources = fetchSources;
exports.selectScenario = selectScenario;
async function getJSON(url) {
  const resp = await fetch(url, {
    headers: {
      Accept: 'application/json'
    }
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
    headers: {
      'Content-Type': 'application/json'
    },
    body: JSON.stringify({
      scenario_id: scenarioId
    })
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
  if (params.severity && params.severity !== 'all') sp.set('severity', params.severity);
  if (params.category && params.category !== 'all') sp.set('category', params.category);
  if (params.q) sp.set('q', params.q);
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
  __modules["./App"] = function(module, exports, require) {
"use strict";

Object.defineProperty(exports, "__esModule", {
  value: true
});
exports.App = App;
var _jsxRuntime = require("./jsx-runtime");
var _api = require("./api");
function getInitialQueryParam(name, fallback) {
  try {
    const sp = new URLSearchParams(window.location.search);
    return sp.get(name) || fallback;
  } catch {
    return fallback;
  }
}
function App() {
  const initialTab = getInitialQueryParam('tab', 'overview');
  const initialScenarioParam = getInitialQueryParam('scenario', '');
  const [activeTab, setActiveTab] = (0, _jsxRuntime.useState)(initialTab);
  const [scenarios, setScenarios] = (0, _jsxRuntime.useState)([]);
  const [selectedScenarioId, setSelectedScenarioId] = (0, _jsxRuntime.useState)(initialScenarioParam);
  const [overview, setOverview] = (0, _jsxRuntime.useState)(null);
  const [rulesList, setRulesList] = (0, _jsxRuntime.useState)([]);
  const [sourcesDetail, setSourcesDetail] = (0, _jsxRuntime.useState)(null);
  const [selectedIncident, setSelectedIncident] = (0, _jsxRuntime.useState)(null);
  const [loading, setLoading] = (0, _jsxRuntime.useState)(true);
  const [errorMsg, setErrorMsg] = (0, _jsxRuntime.useState)('');

  // Incident list filters
  const [severityFilter, setSeverityFilter] = (0, _jsxRuntime.useState)('all');
  const [categoryFilter, setCategoryFilter] = (0, _jsxRuntime.useState)('all');
  const [searchQuery, setSearchQuery] = (0, _jsxRuntime.useState)('');
  (0, _jsxRuntime.useEffect)(() => {
    loadAllData(initialScenarioParam);
  }, []);
  async function loadAllData(targetScenario) {
    setLoading(true);
    setErrorMsg('');
    try {
      let ov;
      if (targetScenario) {
        ov = await (0, _api.selectScenario)(targetScenario);
      } else {
        ov = await (0, _api.fetchOverview)();
      }
      const [scenResp, rulesResp, srcResp] = await Promise.all([(0, _api.fetchScenarios)(), (0, _api.fetchRules)(), (0, _api.fetchSources)()]);
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
    } catch (err) {
      setErrorMsg(err?.message || 'Failed to load cluster diagnostics from backend API.');
    } finally {
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
    return (0, _jsxRuntime.h)("div", {
      className: "main-container"
    }, (0, _jsxRuntime.h)("div", {
      className: "panel"
    }, (0, _jsxRuntime.h)("div", {
      className: "panel-title"
    }, "Loading GPU Lens Diagnostics & Telemetry..."), (0, _jsxRuntime.h)("p", {
      style: {
        color: 'var(--text-secondary)',
        marginTop: '8px'
      }
    }, "Evaluating normalized cluster snapshot against deterministic rule registry.")));
  }
  if (errorMsg) {
    return (0, _jsxRuntime.h)("div", {
      className: "main-container"
    }, (0, _jsxRuntime.h)("div", {
      className: "panel",
      style: {
        borderColor: 'var(--status-critical)'
      }
    }, (0, _jsxRuntime.h)("div", {
      className: "panel-title",
      style: {
        color: '#fca5a5'
      }
    }, "Backend Diagnostic API Error"), (0, _jsxRuntime.h)("p", {
      style: {
        marginTop: '8px'
      },
      className: "mono"
    }, errorMsg), (0, _jsxRuntime.h)("button", {
      className: "btn btn-primary",
      style: {
        marginTop: '12px'
      },
      onClick: () => loadAllData()
    }, "Retry Collection")));
  }
  if (!overview) {
    return (0, _jsxRuntime.h)("div", {
      className: "main-container"
    }, "No cluster snapshot available.");
  }
  const cap = overview.capacity_summary;
  const incidents = overview.active_incidents || [];
  const filteredIncidents = incidents.filter(inc => {
    if (severityFilter !== 'all' && inc.severity !== severityFilter) return false;
    if (categoryFilter !== 'all' && inc.category !== categoryFilter) return false;
    if (searchQuery.trim() !== '') {
      const q = searchQuery.toLowerCase();
      const matchTitle = inc.title.toLowerCase().includes(q);
      const matchSummary = inc.summary.toLowerCase().includes(q);
      const matchRule = inc.rule_id.toLowerCase().includes(q);
      const matchRes = (inc.affected_resources || []).some(r => r.toLowerCase().includes(q));
      if (!matchTitle && !matchSummary && !matchRule && !matchRes) return false;
    }
    return true;
  });
  const isSimulated = overview.data_mode === 'simulated_fixture';
  return (0, _jsxRuntime.h)("div", null, (0, _jsxRuntime.h)("div", {
    className: "sim-banner"
  }, (0, _jsxRuntime.h)("div", {
    style: {
      display: 'flex',
      alignItems: 'center',
      gap: '12px',
      flexWrap: 'wrap'
    }
  }, (0, _jsxRuntime.h)("span", {
    className: "sim-badge"
  }, isSimulated ? 'SIMULATED OFFLINE FIXTURE DATA' : 'LIVE INFRASTRUCTURE TELEMETRY'), (0, _jsxRuntime.h)("span", {
    style: {
      color: '#cbd5e1'
    }
  }, isSimulated ? 'Deterministic offline demonstration — zero AI APIs, zero GPU hardware required. All findings are produced by explicit rule evaluation.' : 'Connected to read-only Kubernetes & Prometheus endpoints.')), (0, _jsxRuntime.h)("div", {
    className: "scenario-bar"
  }, (0, _jsxRuntime.h)("label", {
    style: {
      color: '#93c5fd',
      fontWeight: 600,
      fontSize: '12px'
    }
  }, "Active Scenario:"), (0, _jsxRuntime.h)("select", {
    className: "scenario-select",
    value: selectedScenarioId,
    onChange: handleScenarioChange
  }, scenarios.map(s => (0, _jsxRuntime.h)("option", {
    key: s.id,
    value: s.id,
    selected: s.id === selectedScenarioId
  }, s.name))))), (0, _jsxRuntime.h)("header", {
    className: "top-header"
  }, (0, _jsxRuntime.h)("div", {
    className: "brand-group"
  }, (0, _jsxRuntime.h)("div", {
    className: "brand-logo"
  }, "GL"), (0, _jsxRuntime.h)("div", null, (0, _jsxRuntime.h)("div", {
    className: "brand-title"
  }, "GPU Lens"), (0, _jsxRuntime.h)("div", {
    className: "brand-subtitle"
  }, "GPU Fleet Reliability & Capacity Intelligence \xB7 Cluster:", ' ', (0, _jsxRuntime.h)("span", {
    className: "mono",
    style: {
      color: '#e2e8f0'
    }
  }, overview.cluster_name)))), (0, _jsxRuntime.h)("nav", {
    className: "nav-tabs"
  }, (0, _jsxRuntime.h)("button", {
    className: `nav-tab ${activeTab === 'overview' ? 'active' : ''}`,
    onClick: () => setActiveTab('overview')
  }, "Overview"), (0, _jsxRuntime.h)("button", {
    className: `nav-tab ${activeTab === 'incidents' ? 'active' : ''}`,
    onClick: () => setActiveTab('incidents')
  }, "Incidents ", (0, _jsxRuntime.h)("span", {
    className: "tab-count"
  }, incidents.length)), (0, _jsxRuntime.h)("button", {
    className: `nav-tab ${activeTab === 'incident_detail' ? 'active' : ''}`,
    onClick: () => setActiveTab('incident_detail')
  }, "Incident Detail"), (0, _jsxRuntime.h)("button", {
    className: `nav-tab ${activeTab === 'capacity' ? 'active' : ''}`,
    onClick: () => setActiveTab('capacity')
  }, "Capacity & Utilization"), (0, _jsxRuntime.h)("button", {
    className: `nav-tab ${activeTab === 'sources' ? 'active' : ''}`,
    onClick: () => setActiveTab('sources')
  }, "Data Sources ", (0, _jsxRuntime.h)("span", {
    className: "tab-count"
  }, (overview.sources || []).length)), (0, _jsxRuntime.h)("button", {
    className: `nav-tab ${activeTab === 'rules' ? 'active' : ''}`,
    onClick: () => setActiveTab('rules')
  }, "Rules Registry ", (0, _jsxRuntime.h)("span", {
    className: "tab-count"
  }, rulesList.length)))), (0, _jsxRuntime.h)("main", {
    className: "main-container"
  }, (0, _jsxRuntime.h)("div", {
    className: "scenario-context-card"
  }, (0, _jsxRuntime.h)("div", null, (0, _jsxRuntime.h)("div", {
    style: {
      fontWeight: 700,
      fontSize: '14px',
      color: '#f8fafc'
    }
  }, overview.scenario_name), (0, _jsxRuntime.h)("div", {
    style: {
      color: 'var(--text-secondary)',
      fontSize: '13px',
      marginTop: '2px'
    }
  }, overview.scenario_description)), (0, _jsxRuntime.h)("div", {
    style: {
      textAlign: 'right',
      minWidth: '210px'
    }
  }, (0, _jsxRuntime.h)("div", {
    className: "mono",
    style: {
      fontSize: '11.5px',
      color: 'var(--text-secondary)'
    }
  }, "Snapshot Time: ", overview.collected_at), (0, _jsxRuntime.h)("div", {
    style: {
      marginTop: '4px'
    }
  }, (0, _jsxRuntime.h)("span", {
    className: "badge healthy"
  }, "Read-Only Enforced")))), (0, _jsxRuntime.h)("div", {
    className: `health-banner ${overview.fleet_health_status}`
  }, (0, _jsxRuntime.h)("div", null, (0, _jsxRuntime.h)("div", {
    style: {
      display: 'flex',
      alignItems: 'center',
      gap: '10px'
    }
  }, (0, _jsxRuntime.h)("span", {
    className: `badge ${overview.fleet_health_status}`
  }, overview.fleet_health_status.replace(/_/g, ' ')), (0, _jsxRuntime.h)("strong", {
    style: {
      fontSize: '14.5px'
    }
  }, overview.fleet_health_headline))), (0, _jsxRuntime.h)("div", {
    className: "mono",
    style: {
      fontSize: '12px'
    }
  }, "Critical: ", overview.active_incident_counts?.critical || 0, " \xB7 Warning:", ' ', overview.active_incident_counts?.warning || 0, " \xB7 Inconclusive:", ' ', overview.active_incident_counts?.inconclusive || 0)), activeTab === 'overview' && renderOverviewTab(overview, cap, incidents, openIncidentDetail, setActiveTab), activeTab === 'incidents' && renderIncidentsTab(filteredIncidents, severityFilter, setSeverityFilter, categoryFilter, setCategoryFilter, searchQuery, setSearchQuery, openIncidentDetail), activeTab === 'incident_detail' && renderIncidentDetailTab(selectedIncident, incidents, openIncidentDetail), activeTab === 'capacity' && renderCapacityTab(cap), activeTab === 'sources' && renderSourcesTab(overview, sourcesDetail), activeTab === 'rules' && renderRulesTab(rulesList)));
}
function renderOverviewTab(overview, cap, incidents, openIncidentDetail, setActiveTab) {
  return (0, _jsxRuntime.h)("div", null, (0, _jsxRuntime.h)("div", {
    className: "kpi-grid"
  }, (0, _jsxRuntime.h)("div", {
    className: "kpi-card"
  }, (0, _jsxRuntime.h)("div", {
    className: "kpi-label"
  }, "Total Reported GPU Capacity"), (0, _jsxRuntime.h)("div", {
    className: "kpi-value"
  }, cap.total_allocatable_gpus, " / ", cap.total_capacity_gpus, ' ', (0, _jsxRuntime.h)("span", {
    style: {
      fontSize: '13px',
      fontWeight: 500,
      color: 'var(--text-secondary)'
    }
  }, "Allocatable")), (0, _jsxRuntime.h)("div", {
    className: "kpi-sub"
  }, cap.unavailable_hardware_gpus > 0 ? `${cap.unavailable_hardware_gpus} GPU(s) de-allocated by device plugin` : '100% of physical GPUs allocatable')), (0, _jsxRuntime.h)("div", {
    className: "kpi-card"
  }, (0, _jsxRuntime.h)("div", {
    className: "kpi-label"
  }, "Allocated vs Free GPUs"), (0, _jsxRuntime.h)("div", {
    className: "kpi-value"
  }, cap.total_allocated_gpus, " Allocated \xB7 ", cap.total_free_allocatable_gpus, " Free"), (0, _jsxRuntime.h)("div", {
    className: "kpi-sub"
  }, "Pending GPU Requests: ", (0, _jsxRuntime.h)("strong", null, cap.pending_requested_gpus, " GPUs"))), (0, _jsxRuntime.h)("div", {
    className: "kpi-card"
  }, (0, _jsxRuntime.h)("div", {
    className: "kpi-label"
  }, "Active Diagnostic Findings"), (0, _jsxRuntime.h)("div", {
    className: "kpi-value"
  }, incidents.length), (0, _jsxRuntime.h)("div", {
    className: "kpi-sub"
  }, overview.active_incident_counts?.critical || 0, " critical,", ' ', overview.active_incident_counts?.warning || 0, " warning,", ' ', overview.active_incident_counts?.inconclusive || 0, " inconclusive")), (0, _jsxRuntime.h)("div", {
    className: "kpi-card"
  }, (0, _jsxRuntime.h)("div", {
    className: "kpi-label"
  }, "Cluster Mean GPU Utilization"), (0, _jsxRuntime.h)("div", {
    className: "kpi-value"
  }, cap.cluster_mean_gpu_util_percent !== undefined && cap.cluster_mean_gpu_util_percent !== null ? `${cap.cluster_mean_gpu_util_percent}%` : 'N/A (Stale)'), (0, _jsxRuntime.h)("div", {
    className: "kpi-sub"
  }, "Mean VRAM Used:", ' ', cap.cluster_mean_fb_used_percent !== undefined && cap.cluster_mean_fb_used_percent !== null ? `${cap.cluster_mean_fb_used_percent}%` : 'N/A')), (0, _jsxRuntime.h)("div", {
    className: "kpi-card"
  }, (0, _jsxRuntime.h)("div", {
    className: "kpi-label"
  }, "Low-Util GPU-Hours (<15%)"), (0, _jsxRuntime.h)("div", {
    className: "kpi-value"
  }, cap.observed_low_util_gpu_hours, " GPU-h"), (0, _jsxRuntime.h)("div", {
    className: "kpi-sub"
  }, "Out of ", cap.total_observed_allocated_gpu_hours, " mapped GPU-h observed")), (0, _jsxRuntime.h)("div", {
    className: "kpi-card"
  }, (0, _jsxRuntime.h)("div", {
    className: "kpi-label"
  }, "Telemetry & Pod Attribution"), (0, _jsxRuntime.h)("div", {
    className: "kpi-value"
  }, cap.telemetry_fresh_gpus, " Fresh / ", cap.telemetry_stale_gpus, " Stale"), (0, _jsxRuntime.h)("div", {
    className: "kpi-sub"
  }, cap.unmapped_allocated_gpus > 0 ? `${cap.unmapped_allocated_gpus} allocated GPU(s) missing pod labels` : 'All allocated GPUs mapped to pods'))), cap.attribution_blind_spot_warning && (0, _jsxRuntime.h)("div", {
    className: "blindspot-box"
  }, (0, _jsxRuntime.h)("strong", null, "Observability Blind Spot Warning:"), " ", cap.attribution_blind_spot_warning), (0, _jsxRuntime.h)("div", {
    className: "section-grid-2"
  }, (0, _jsxRuntime.h)("div", {
    className: "panel"
  }, (0, _jsxRuntime.h)("div", {
    className: "panel-header"
  }, (0, _jsxRuntime.h)("div", {
    className: "panel-title"
  }, "Active Diagnostic Incidents (", incidents.length, ")"), (0, _jsxRuntime.h)("button", {
    className: "btn",
    onClick: () => setActiveTab('incidents')
  }, "View All / Filter")), incidents.length === 0 ? (0, _jsxRuntime.h)("div", {
    className: "empty-state"
  }, (0, _jsxRuntime.h)("strong", null, "Zero Active Incidents"), (0, _jsxRuntime.h)("p", {
    style: {
      marginTop: '4px'
    }
  }, "All 6 diagnostic rules evaluated against fresh cluster telemetry with zero threshold violations.")) : (0, _jsxRuntime.h)("table", {
    className: "data-table"
  }, (0, _jsxRuntime.h)("thead", null, (0, _jsxRuntime.h)("tr", null, (0, _jsxRuntime.h)("th", null, "Severity"), (0, _jsxRuntime.h)("th", null, "Rule"), (0, _jsxRuntime.h)("th", null, "Finding & Summary"), (0, _jsxRuntime.h)("th", null, "Evidence Strength"), (0, _jsxRuntime.h)("th", null, "Action"))), (0, _jsxRuntime.h)("tbody", null, incidents.map(inc => (0, _jsxRuntime.h)("tr", {
    key: inc.id,
    className: "incident-row",
    onClick: () => openIncidentDetail(inc)
  }, (0, _jsxRuntime.h)("td", null, (0, _jsxRuntime.h)("span", {
    className: `badge ${inc.severity}`
  }, inc.severity), inc.eval_status === 'inconclusive' && (0, _jsxRuntime.h)("div", {
    style: {
      marginTop: '4px'
    }
  }, (0, _jsxRuntime.h)("span", {
    className: "badge inconclusive"
  }, "inconclusive"))), (0, _jsxRuntime.h)("td", {
    className: "mono"
  }, inc.rule_id), (0, _jsxRuntime.h)("td", null, (0, _jsxRuntime.h)("div", {
    style: {
      fontWeight: 700,
      color: '#f8fafc'
    }
  }, inc.title), (0, _jsxRuntime.h)("div", {
    style: {
      color: 'var(--text-secondary)',
      fontSize: '12px',
      marginTop: '2px'
    }
  }, inc.summary)), (0, _jsxRuntime.h)("td", null, (0, _jsxRuntime.h)("span", {
    className: `badge ${inc.evidence_strength}`
  }, inc.evidence_strength)), (0, _jsxRuntime.h)("td", null, (0, _jsxRuntime.h)("button", {
    className: "btn btn-primary",
    onClick: e => {
      e.stopPropagation();
      openIncidentDetail(inc);
    }
  }, "Inspect"))))))), (0, _jsxRuntime.h)("div", null, (0, _jsxRuntime.h)("div", {
    className: "panel"
  }, (0, _jsxRuntime.h)("div", {
    className: "panel-header"
  }, (0, _jsxRuntime.h)("div", {
    className: "panel-title"
  }, "Node GPU Capacity & Health Summary"), (0, _jsxRuntime.h)("button", {
    className: "btn",
    onClick: () => setActiveTab('capacity')
  }, "Capacity Report")), (0, _jsxRuntime.h)("table", {
    className: "data-table"
  }, (0, _jsxRuntime.h)("thead", null, (0, _jsxRuntime.h)("tr", null, (0, _jsxRuntime.h)("th", null, "Node"), (0, _jsxRuntime.h)("th", null, "Cap / Alloc / Used"), (0, _jsxRuntime.h)("th", null, "Mean Util"), (0, _jsxRuntime.h)("th", null, "Status"))), (0, _jsxRuntime.h)("tbody", null, (cap.nodes || []).map(n => (0, _jsxRuntime.h)("tr", {
    key: n.node_name
  }, (0, _jsxRuntime.h)("td", null, (0, _jsxRuntime.h)("div", {
    className: "mono",
    style: {
      fontWeight: 700
    }
  }, n.node_name), (0, _jsxRuntime.h)("div", {
    style: {
      fontSize: '11.5px',
      color: 'var(--text-secondary)'
    }
  }, n.gpu_model)), (0, _jsxRuntime.h)("td", {
    className: "mono"
  }, n.capacity_gpus, " / ", n.allocatable_gpus, " / ", n.allocated_gpus, n.unavailable_gpus > 0 && (0, _jsxRuntime.h)("span", {
    style: {
      color: '#fca5a5',
      display: 'block'
    }
  }, "(-", n.unavailable_gpus, " unavail)")), (0, _jsxRuntime.h)("td", {
    className: "mono"
  }, n.mean_gpu_util_percent !== undefined && n.mean_gpu_util_percent !== null ? `${n.mean_gpu_util_percent}%` : 'Stale'), (0, _jsxRuntime.h)("td", null, (0, _jsxRuntime.h)("span", {
    className: `badge ${n.status_note.includes('Healthy') ? 'healthy' : 'warning'}`
  }, n.status_note))))))), (0, _jsxRuntime.h)("div", {
    className: "panel"
  }, (0, _jsxRuntime.h)("div", {
    className: "panel-header"
  }, (0, _jsxRuntime.h)("div", {
    className: "panel-title"
  }, "Pending GPU Workloads (", (cap.pending_workloads || []).length, ")")), (cap.pending_workloads || []).length === 0 ? (0, _jsxRuntime.h)("div", {
    style: {
      color: 'var(--text-secondary)',
      fontSize: '13px'
    }
  }, "No pending GPU workloads in queue.") : (0, _jsxRuntime.h)("table", {
    className: "data-table"
  }, (0, _jsxRuntime.h)("thead", null, (0, _jsxRuntime.h)("tr", null, (0, _jsxRuntime.h)("th", null, "Workload Pod"), (0, _jsxRuntime.h)("th", null, "Req GPUs"), (0, _jsxRuntime.h)("th", null, "Wait"), (0, _jsxRuntime.h)("th", null, "Observed Scheduler Reason"))), (0, _jsxRuntime.h)("tbody", null, (cap.pending_workloads || []).map(pw => (0, _jsxRuntime.h)("tr", {
    key: `${pw.namespace}/${pw.pod_name}`
  }, (0, _jsxRuntime.h)("td", {
    className: "mono"
  }, pw.namespace, "/", pw.pod_name), (0, _jsxRuntime.h)("td", {
    className: "mono"
  }, pw.requested_gpus), (0, _jsxRuntime.h)("td", {
    className: "mono"
  }, pw.pending_minutes, "m"), (0, _jsxRuntime.h)("td", {
    style: {
      fontSize: '12px',
      color: '#fcd34d'
    }
  }, (0, _jsxRuntime.h)("strong", null, pw.observed_reason, ":"), " ", pw.scheduler_message)))))))));
}
function renderIncidentsTab(incidents, severityFilter, setSeverityFilter, categoryFilter, setCategoryFilter, searchQuery, setSearchQuery, openIncidentDetail) {
  return (0, _jsxRuntime.h)("div", {
    className: "panel"
  }, (0, _jsxRuntime.h)("div", {
    className: "panel-header"
  }, (0, _jsxRuntime.h)("div", {
    className: "panel-title"
  }, "Searchable & Filterable Incident Queue"), (0, _jsxRuntime.h)("span", {
    className: "mono",
    style: {
      color: 'var(--text-secondary)'
    }
  }, "Showing ", incidents.length, " incident(s)")), (0, _jsxRuntime.h)("div", {
    className: "filter-bar"
  }, (0, _jsxRuntime.h)("input", {
    type: "text",
    className: "filter-input",
    placeholder: "Search by pod, GPU UUID, node, XID, or rule ID...",
    value: searchQuery,
    onInput: e => setSearchQuery(e.target.value)
  }), (0, _jsxRuntime.h)("select", {
    className: "filter-select",
    value: severityFilter,
    onChange: e => setSeverityFilter(e.target.value)
  }, (0, _jsxRuntime.h)("option", {
    value: "all"
  }, "All Severities"), (0, _jsxRuntime.h)("option", {
    value: "critical"
  }, "Critical Only"), (0, _jsxRuntime.h)("option", {
    value: "warning"
  }, "Warning Only"), (0, _jsxRuntime.h)("option", {
    value: "info"
  }, "Info Only")), (0, _jsxRuntime.h)("select", {
    className: "filter-select",
    value: categoryFilter,
    onChange: e => setCategoryFilter(e.target.value)
  }, (0, _jsxRuntime.h)("option", {
    value: "all"
  }, "All Categories"), (0, _jsxRuntime.h)("option", {
    value: "scheduling"
  }, "Scheduling (GPU-SCHED-001)"), (0, _jsxRuntime.h)("option", {
    value: "hardware_health"
  }, "Hardware Health (GPU-HEALTH-002)"), (0, _jsxRuntime.h)("option", {
    value: "utilization"
  }, "Low Utilization (GPU-UTIL-003)"), (0, _jsxRuntime.h)("option", {
    value: "failure_correlation"
  }, "Failure Correlation (GPU-CORR-004)"), (0, _jsxRuntime.h)("option", {
    value: "data_freshness"
  }, "Data Freshness (GPU-FRESH-005)"), (0, _jsxRuntime.h)("option", {
    value: "thermal_power_memory"
  }, "Thermal / Power / Memory (GPU-THERM-006)"))), incidents.length === 0 ? (0, _jsxRuntime.h)("div", {
    className: "empty-state"
  }, (0, _jsxRuntime.h)("strong", null, "No Incidents Match Current Filters"), (0, _jsxRuntime.h)("p", {
    style: {
      marginTop: '4px'
    }
  }, "Try clearing the search filter or selecting another scenario in the top banner.")) : (0, _jsxRuntime.h)("table", {
    className: "data-table"
  }, (0, _jsxRuntime.h)("thead", null, (0, _jsxRuntime.h)("tr", null, (0, _jsxRuntime.h)("th", null, "Incident ID / Fingerprint"), (0, _jsxRuntime.h)("th", null, "Severity & Status"), (0, _jsxRuntime.h)("th", null, "Rule"), (0, _jsxRuntime.h)("th", null, "Title & Summary"), (0, _jsxRuntime.h)("th", null, "Evidence Strength"), (0, _jsxRuntime.h)("th", null, "Occurrences"), (0, _jsxRuntime.h)("th", null, "Inspect"))), (0, _jsxRuntime.h)("tbody", null, incidents.map(inc => (0, _jsxRuntime.h)("tr", {
    key: inc.id,
    className: "incident-row",
    onClick: () => openIncidentDetail(inc)
  }, (0, _jsxRuntime.h)("td", {
    className: "mono"
  }, (0, _jsxRuntime.h)("div", null, inc.id), (0, _jsxRuntime.h)("div", {
    style: {
      color: 'var(--text-muted)',
      fontSize: '11px'
    }
  }, "fp:", inc.fingerprint)), (0, _jsxRuntime.h)("td", null, (0, _jsxRuntime.h)("span", {
    className: `badge ${inc.severity}`
  }, inc.severity), (0, _jsxRuntime.h)("div", {
    style: {
      marginTop: '4px'
    }
  }, (0, _jsxRuntime.h)("span", {
    className: `badge ${inc.eval_status}`
  }, inc.eval_status))), (0, _jsxRuntime.h)("td", {
    className: "mono"
  }, (0, _jsxRuntime.h)("div", null, inc.rule_id), (0, _jsxRuntime.h)("div", {
    style: {
      fontSize: '11px',
      color: 'var(--text-secondary)'
    }
  }, inc.category)), (0, _jsxRuntime.h)("td", null, (0, _jsxRuntime.h)("div", {
    style: {
      fontWeight: 700
    }
  }, inc.title), (0, _jsxRuntime.h)("div", {
    style: {
      color: 'var(--text-secondary)',
      fontSize: '12px',
      marginTop: '3px'
    }
  }, inc.summary), (0, _jsxRuntime.h)("div", {
    style: {
      marginTop: '5px',
      display: 'flex',
      gap: '6px',
      flexWrap: 'wrap'
    }
  }, (inc.affected_resources || []).map(r => (0, _jsxRuntime.h)("span", {
    key: r,
    className: "mono",
    style: {
      background: 'var(--bg-elevated)',
      padding: '1px 6px',
      borderRadius: '4px',
      fontSize: '11px'
    }
  }, r)))), (0, _jsxRuntime.h)("td", null, (0, _jsxRuntime.h)("span", {
    className: `badge ${inc.evidence_strength}`
  }, inc.evidence_strength)), (0, _jsxRuntime.h)("td", {
    className: "mono"
  }, inc.occurrence_count, "x"), (0, _jsxRuntime.h)("td", null, (0, _jsxRuntime.h)("button", {
    className: "btn btn-primary",
    onClick: e => {
      e.stopPropagation();
      openIncidentDetail(inc);
    }
  }, "Open Evidence")))))));
}
function renderIncidentDetailTab(selected, allIncidents, onSelect) {
  if (!selected) {
    return (0, _jsxRuntime.h)("div", {
      className: "panel"
    }, (0, _jsxRuntime.h)("div", {
      className: "empty-state"
    }, (0, _jsxRuntime.h)("strong", null, "No Active Incident Selected"), (0, _jsxRuntime.h)("p", {
      style: {
        marginTop: '6px'
      }
    }, "The currently selected scenario has zero active incidents, or none is selected. Switch to Scenarios 2\u20136 to inspect detailed multi-signal incident reports.")));
  }
  return (0, _jsxRuntime.h)("div", null, allIncidents.length > 1 && (0, _jsxRuntime.h)("div", {
    className: "panel",
    style: {
      padding: '10px 16px'
    }
  }, (0, _jsxRuntime.h)("div", {
    style: {
      display: 'flex',
      alignItems: 'center',
      gap: '8px',
      flexWrap: 'wrap'
    }
  }, (0, _jsxRuntime.h)("span", {
    style: {
      fontSize: '12px',
      color: 'var(--text-secondary)',
      fontWeight: 600
    }
  }, "Switch Incident in Scenario (", allIncidents.length, "):"), allIncidents.map(inc => (0, _jsxRuntime.h)("button", {
    key: inc.id,
    className: `btn ${inc.id === selected.id ? 'btn-primary' : ''}`,
    onClick: () => onSelect(inc)
  }, "[", inc.rule_id, "] ", inc.title.slice(0, 48), "...")))), (0, _jsxRuntime.h)("div", {
    className: "panel"
  }, (0, _jsxRuntime.h)("div", {
    className: "panel-header"
  }, (0, _jsxRuntime.h)("div", null, (0, _jsxRuntime.h)("div", {
    style: {
      display: 'flex',
      alignItems: 'center',
      gap: '8px',
      marginBottom: '6px'
    }
  }, (0, _jsxRuntime.h)("span", {
    className: `badge ${selected.severity}`
  }, selected.severity), (0, _jsxRuntime.h)("span", {
    className: `badge ${selected.eval_status}`
  }, selected.eval_status), (0, _jsxRuntime.h)("span", {
    className: `badge ${selected.evidence_strength}`
  }, "Evidence: ", selected.evidence_strength), (0, _jsxRuntime.h)("span", {
    className: "mono",
    style: {
      color: 'var(--text-secondary)'
    }
  }, "Rule: ", selected.rule_id, " (", selected.rule_name, ")")), (0, _jsxRuntime.h)("h2", {
    style: {
      fontSize: '18px',
      fontWeight: 700
    }
  }, selected.title)), (0, _jsxRuntime.h)("div", {
    className: "mono",
    style: {
      textAlign: 'right',
      fontSize: '12px',
      color: 'var(--text-secondary)'
    }
  }, (0, _jsxRuntime.h)("div", null, "ID: ", selected.id), (0, _jsxRuntime.h)("div", null, "Fingerprint: ", selected.fingerprint), (0, _jsxRuntime.h)("div", null, "Occurrences: ", selected.occurrence_count))), (0, _jsxRuntime.h)("p", {
    style: {
      fontSize: '14px',
      color: '#e2e8f0',
      marginBottom: '12px'
    }
  }, selected.summary), (0, _jsxRuntime.h)("div", {
    className: "disclaimer-box"
  }, (0, _jsxRuntime.h)("strong", null, "Evidence Strength Basis (", selected.evidence_strength.toUpperCase(), "):"), ' ', selected.evidence_strength_basis, selected.inconclusive_reason && (0, _jsxRuntime.h)("div", {
    style: {
      marginTop: '4px',
      color: '#e9d5ff'
    }
  }, (0, _jsxRuntime.h)("strong", null, "Inconclusive Safeguard:"), " ", selected.inconclusive_reason))), (0, _jsxRuntime.h)("div", {
    className: "section-grid-2"
  }, (0, _jsxRuntime.h)("div", {
    className: "panel"
  }, (0, _jsxRuntime.h)("div", {
    className: "panel-header"
  }, (0, _jsxRuntime.h)("div", {
    className: "panel-title"
  }, "Observed Facts (Verified Signals)"), (0, _jsxRuntime.h)("span", {
    className: "badge healthy"
  }, "Directly Measured")), (0, _jsxRuntime.h)("ul", {
    className: "bullet-list"
  }, (selected.observed_facts || []).map((f, i) => (0, _jsxRuntime.h)("li", {
    key: i
  }, f))), (0, _jsxRuntime.h)("div", {
    className: "panel-header",
    style: {
      marginTop: '18px'
    }
  }, (0, _jsxRuntime.h)("div", {
    className: "panel-title"
  }, "Possible Explanations (Hypotheses to Verify \u2014 Not Proven Causation)"), (0, _jsxRuntime.h)("span", {
    className: "badge info"
  }, "Hypotheses")), (0, _jsxRuntime.h)("ul", {
    className: "bullet-list"
  }, (selected.possible_causes || []).map((c, i) => (0, _jsxRuntime.h)("li", {
    key: i
  }, c)))), (0, _jsxRuntime.h)("div", {
    className: "panel"
  }, (0, _jsxRuntime.h)("div", {
    className: "panel-header"
  }, (0, _jsxRuntime.h)("div", {
    className: "panel-title"
  }, "Supporting Evidence (", (selected.supporting_evidence || []).length, ")")), (selected.supporting_evidence || []).map((ev, i) => (0, _jsxRuntime.h)("div", {
    key: i,
    className: "evidence-box supporting"
  }, (0, _jsxRuntime.h)("div", {
    style: {
      display: 'flex',
      justifyContent: 'space-between',
      fontSize: '12px'
    }
  }, (0, _jsxRuntime.h)("span", {
    className: "mono",
    style: {
      color: '#fca5a5',
      fontWeight: 700
    }
  }, "[", ev.kind, "] ", ev.object_ref, " ", ev.metric_name ? `· ${ev.metric_name}` : ''), (0, _jsxRuntime.h)("span", {
    className: "mono",
    style: {
      color: 'var(--text-secondary)'
    }
  }, ev.observed_at)), (0, _jsxRuntime.h)("div", {
    style: {
      marginTop: '4px',
      fontWeight: 600
    }
  }, ev.summary), (0, _jsxRuntime.h)("div", {
    className: "mono",
    style: {
      marginTop: '3px',
      fontSize: '11.5px',
      color: 'var(--text-secondary)'
    }
  }, "Observed Value: ", ev.value, " ", ev.threshold ? `| Rule Threshold: ${ev.threshold}` : ''))), (0, _jsxRuntime.h)("div", {
    className: "panel-header",
    style: {
      marginTop: '16px'
    }
  }, (0, _jsxRuntime.h)("div", {
    className: "panel-title"
  }, "Contradicting & Contextual Evidence (", (selected.contradicting_evidence || []).length, ")")), (selected.contradicting_evidence || []).length === 0 ? (0, _jsxRuntime.h)("div", {
    style: {
      color: 'var(--text-secondary)',
      fontSize: '12.5px'
    }
  }, "No contradicting signals observed for this incident.") : (selected.contradicting_evidence || []).map((ev, i) => (0, _jsxRuntime.h)("div", {
    key: i,
    className: `evidence-box ${ev.polarity}`
  }, (0, _jsxRuntime.h)("div", {
    style: {
      display: 'flex',
      justifyContent: 'space-between',
      fontSize: '12px'
    }
  }, (0, _jsxRuntime.h)("span", {
    className: "mono",
    style: {
      color: '#6ee7b7',
      fontWeight: 700
    }
  }, "[", ev.polarity.toUpperCase(), "] ", ev.object_ref, ' ', ev.metric_name ? `· ${ev.metric_name}` : ''), (0, _jsxRuntime.h)("span", {
    className: "mono",
    style: {
      color: 'var(--text-secondary)'
    }
  }, ev.observed_at)), (0, _jsxRuntime.h)("div", {
    style: {
      marginTop: '4px',
      fontWeight: 600
    }
  }, ev.summary), (0, _jsxRuntime.h)("div", {
    className: "mono",
    style: {
      marginTop: '3px',
      fontSize: '11.5px',
      color: 'var(--text-secondary)'
    }
  }, "Value: ", ev.value))))), (0, _jsxRuntime.h)("div", {
    className: "section-grid-2"
  }, (0, _jsxRuntime.h)("div", {
    className: "panel"
  }, (0, _jsxRuntime.h)("div", {
    className: "panel-header"
  }, (0, _jsxRuntime.h)("div", {
    className: "panel-title"
  }, "Correlated Multi-Signal Timeline")), (selected.timeline || []).length === 0 ? (0, _jsxRuntime.h)("div", {
    style: {
      color: 'var(--text-secondary)'
    }
  }, "No discrete timeline transitions recorded.") : (0, _jsxRuntime.h)("div", {
    className: "timeline-list"
  }, (selected.timeline || []).map((t, i) => (0, _jsxRuntime.h)("div", {
    key: i,
    className: "timeline-item"
  }, (0, _jsxRuntime.h)("span", {
    className: "mono",
    style: {
      color: 'var(--text-secondary)'
    }
  }, t.timestamp), (0, _jsxRuntime.h)("span", {
    className: `badge ${t.severity}`
  }, t.layer), (0, _jsxRuntime.h)("div", null, (0, _jsxRuntime.h)("strong", {
    className: "mono"
  }, t.event_type), " (", t.object_ref, "): ", t.summary))))), (0, _jsxRuntime.h)("div", {
    className: "panel"
  }, (0, _jsxRuntime.h)("div", {
    className: "panel-header"
  }, (0, _jsxRuntime.h)("div", {
    className: "panel-title"
  }, "Recommended Engineering Next Checks")), (0, _jsxRuntime.h)("ul", {
    className: "bullet-list"
  }, (selected.suggested_checks || []).map((chk, i) => (0, _jsxRuntime.h)("li", {
    key: i
  }, chk))), (0, _jsxRuntime.h)("div", {
    className: "panel-header",
    style: {
      marginTop: '18px'
    }
  }, (0, _jsxRuntime.h)("div", {
    className: "panel-title"
  }, "Affected Infrastructure Resources")), (0, _jsxRuntime.h)("div", {
    style: {
      display: 'flex',
      gap: '8px',
      flexWrap: 'wrap'
    }
  }, (selected.affected_resources || []).map(res => (0, _jsxRuntime.h)("span", {
    key: res,
    className: "mono",
    style: {
      background: 'var(--bg-elevated)',
      border: '1px solid var(--border-strong)',
      padding: '4px 10px',
      borderRadius: '5px'
    }
  }, res))))));
}
function renderCapacityTab(cap) {
  const history = cap.historical_utilization || [];
  const meth = cap.methodology || {};
  return (0, _jsxRuntime.h)("div", null, cap.attribution_blind_spot_warning && (0, _jsxRuntime.h)("div", {
    className: "blindspot-box"
  }, (0, _jsxRuntime.h)("strong", null, "Telemetry Attribution Blind Spot:"), " ", cap.attribution_blind_spot_warning), (0, _jsxRuntime.h)("div", {
    className: "kpi-grid"
  }, (0, _jsxRuntime.h)("div", {
    className: "kpi-card"
  }, (0, _jsxRuntime.h)("div", {
    className: "kpi-label"
  }, "Physical / Allocatable GPUs"), (0, _jsxRuntime.h)("div", {
    className: "kpi-value"
  }, cap.total_capacity_gpus, " / ", cap.total_allocatable_gpus), (0, _jsxRuntime.h)("div", {
    className: "kpi-sub"
  }, "Unavailable Hardware GPUs: ", cap.unavailable_hardware_gpus)), (0, _jsxRuntime.h)("div", {
    className: "kpi-card"
  }, (0, _jsxRuntime.h)("div", {
    className: "kpi-label"
  }, "Allocated / Free Allocatable"), (0, _jsxRuntime.h)("div", {
    className: "kpi-value"
  }, cap.total_allocated_gpus, " / ", cap.total_free_allocatable_gpus), (0, _jsxRuntime.h)("div", {
    className: "kpi-sub"
  }, "Pending Queue Requests: ", cap.pending_requested_gpus, " GPUs")), (0, _jsxRuntime.h)("div", {
    className: "kpi-card"
  }, (0, _jsxRuntime.h)("div", {
    className: "kpi-label"
  }, "Observed Allocated GPU-Hours"), (0, _jsxRuntime.h)("div", {
    className: "kpi-value"
  }, cap.total_observed_allocated_gpu_hours, " GPU-h"), (0, _jsxRuntime.h)("div", {
    className: "kpi-sub"
  }, "Below ", meth.util_threshold_percent, "% SM threshold: ", (0, _jsxRuntime.h)("strong", null, cap.observed_low_util_gpu_hours, " GPU-h"))), (0, _jsxRuntime.h)("div", {
    className: "kpi-card"
  }, (0, _jsxRuntime.h)("div", {
    className: "kpi-label"
  }, "Illustrative Sample Cost Est."), (0, _jsxRuntime.h)("div", {
    className: "kpi-value"
  }, cap.optional_low_util_sample_cost_usd !== undefined && cap.optional_low_util_sample_cost_usd !== null ? `$${cap.optional_low_util_sample_cost_usd.toFixed(2)}` : 'Disabled'), (0, _jsxRuntime.h)("div", {
    className: "kpi-sub"
  }, "Sample rate: $", meth.optional_sample_hourly_rate_usd?.toFixed(2) || '0.00', "/GPU-h (Estimate only \u2014 NOT realized savings)"))), (0, _jsxRuntime.h)("div", {
    className: "panel"
  }, (0, _jsxRuntime.h)("div", {
    className: "panel-header"
  }, (0, _jsxRuntime.h)("div", {
    className: "panel-title"
  }, "Historical Cluster GPU Utilization vs. Framebuffer VRAM Residency (", history.length, " time-series buckets)"), (0, _jsxRuntime.h)("span", {
    className: "mono",
    style: {
      fontSize: '12px',
      color: 'var(--text-secondary)'
    }
  }, "Blue = Mean SM Compute Util (%) \xB7 Emerald = Mean Framebuffer VRAM Used (%)")), history.length < 3 ? (0, _jsxRuntime.h)("div", {
    className: "empty-state"
  }, (0, _jsxRuntime.h)("strong", null, "Insufficient Historical Samples for Chart Rendering"), (0, _jsxRuntime.h)("p", {
    style: {
      marginTop: '4px'
    }
  }, "GPU Lens renders historical charts only when at least 3 fresh time-series buckets exist.")) : renderUtilizationSVGChart(history, meth.util_threshold_percent || 15)), (0, _jsxRuntime.h)("div", {
    className: "panel"
  }, (0, _jsxRuntime.h)("div", {
    className: "panel-header"
  }, (0, _jsxRuntime.h)("div", {
    className: "panel-title"
  }, "Persistently Low-Utilization Allocation Windows (", (cap.low_utilization_workloads || []).length, ")"), (0, _jsxRuntime.h)("span", {
    className: "badge info"
  }, "Threshold < ", meth.util_threshold_percent, "% over >= ", meth.min_window_minutes, "m")), (cap.low_utilization_workloads || []).length === 0 ? (0, _jsxRuntime.h)("div", {
    className: "empty-state"
  }, (0, _jsxRuntime.h)("strong", null, "No Persistently Low-Utilization Workloads Observed"), (0, _jsxRuntime.h)("p", {
    style: {
      marginTop: '4px'
    }
  }, "All mapped running workloads either exceed the ", meth.util_threshold_percent, "% SM compute threshold or lack complete pod attribution labels.")) : (0, _jsxRuntime.h)("div", null, (0, _jsxRuntime.h)("table", {
    className: "data-table"
  }, (0, _jsxRuntime.h)("thead", null, (0, _jsxRuntime.h)("tr", null, (0, _jsxRuntime.h)("th", null, "Workload Pod & Owner"), (0, _jsxRuntime.h)("th", null, "Node & GPUs"), (0, _jsxRuntime.h)("th", null, "Window & Samples"), (0, _jsxRuntime.h)("th", null, "SM Compute Util"), (0, _jsxRuntime.h)("th", null, "Mem Copy & VRAM Used"), (0, _jsxRuntime.h)("th", null, "Low-Util GPU-Hours"), (0, _jsxRuntime.h)("th", null, "Workload Context & Follow-Up Checklist"))), (0, _jsxRuntime.h)("tbody", null, (cap.low_utilization_workloads || []).map(w => (0, _jsxRuntime.h)("tr", {
    key: `${w.namespace}/${w.pod_name}`
  }, (0, _jsxRuntime.h)("td", null, (0, _jsxRuntime.h)("div", {
    className: "mono",
    style: {
      fontWeight: 700
    }
  }, w.namespace, "/", w.pod_name), (0, _jsxRuntime.h)("div", {
    style: {
      fontSize: '11.5px',
      color: 'var(--text-secondary)'
    }
  }, w.owner_ref)), (0, _jsxRuntime.h)("td", {
    className: "mono"
  }, (0, _jsxRuntime.h)("div", null, w.node_name), (0, _jsxRuntime.h)("div", {
    style: {
      fontSize: '11px',
      color: 'var(--text-secondary)'
    }
  }, w.allocated_gpus, " GPU(s): ", (w.assigned_gpu_uuids || []).join(', '))), (0, _jsxRuntime.h)("td", {
    className: "mono"
  }, (0, _jsxRuntime.h)("div", null, w.observation_window_minutes, " min"), (0, _jsxRuntime.h)("div", {
    style: {
      fontSize: '11px',
      color: 'var(--text-secondary)'
    }
  }, w.sample_count, " samples (last: ", w.last_data_update, ")")), (0, _jsxRuntime.h)("td", {
    className: "mono"
  }, (0, _jsxRuntime.h)("strong", {
    style: {
      color: '#fcd34d'
    }
  }, w.mean_gpu_util_percent, "% mean"), (0, _jsxRuntime.h)("div", {
    style: {
      fontSize: '11px',
      color: 'var(--text-secondary)'
    }
  }, "min ", w.min_gpu_util_percent, "% / max ", w.max_gpu_util_percent, "%")), (0, _jsxRuntime.h)("td", {
    className: "mono"
  }, (0, _jsxRuntime.h)("div", null, "VRAM: ", w.mean_fb_used_mib, " MiB (", w.mean_fb_used_percent, "%)"), (0, _jsxRuntime.h)("div", {
    style: {
      fontSize: '11px',
      color: 'var(--text-secondary)'
    }
  }, "MemCopy: ", w.mean_mem_copy_util_percent, "%")), (0, _jsxRuntime.h)("td", {
    className: "mono"
  }, (0, _jsxRuntime.h)("div", null, w.low_util_gpu_hours, " GPU-h"), w.optional_sample_cost_estimate_usd !== undefined && (0, _jsxRuntime.h)("div", {
    style: {
      fontSize: '11px',
      color: 'var(--text-secondary)'
    }
  }, "Sample est: $", w.optional_sample_cost_estimate_usd.toFixed(2))), (0, _jsxRuntime.h)("td", {
    style: {
      maxWidth: '380px'
    }
  }, (0, _jsxRuntime.h)("div", {
    style: {
      fontWeight: 600,
      fontSize: '12px',
      color: '#fde68a',
      marginBottom: '4px'
    }
  }, w.workload_context_note), (0, _jsxRuntime.h)("ul", {
    className: "bullet-list",
    style: {
      fontSize: '11.5px',
      color: 'var(--text-secondary)'
    }
  }, (w.follow_up_checklist || []).map((item, idx) => (0, _jsxRuntime.h)("li", {
    key: idx
  }, item))))))))), (0, _jsxRuntime.h)("div", {
    className: "disclaimer-box"
  }, (0, _jsxRuntime.h)("strong", null, "Non-Waste Engineering Policy:"), " ", meth.non_waste_disclaimer)), (0, _jsxRuntime.h)("div", {
    className: "panel"
  }, (0, _jsxRuntime.h)("div", {
    className: "panel-header"
  }, (0, _jsxRuntime.h)("div", {
    className: "panel-title"
  }, "Transparent Capacity & Low-Utilization Calculation Methodology")), (0, _jsxRuntime.h)("p", {
    className: "mono",
    style: {
      marginBottom: '10px',
      color: '#7dd3fc'
    }
  }, meth.formula_summary), (0, _jsxRuntime.h)("ul", {
    className: "bullet-list",
    style: {
      marginBottom: '10px'
    }
  }, (meth.step_by_step_calculation || []).map((s, i) => (0, _jsxRuntime.h)("li", {
    key: i
  }, s))), (0, _jsxRuntime.h)("p", {
    style: {
      fontSize: '12.5px',
      color: 'var(--text-secondary)'
    }
  }, (0, _jsxRuntime.h)("strong", null, "Unmapped Telemetry Policy:"), " ", meth.missing_mapping_policy), meth.optional_cost_estimate_label && (0, _jsxRuntime.h)("p", {
    style: {
      fontSize: '12.5px',
      color: '#fcd34d',
      marginTop: '6px'
    }
  }, (0, _jsxRuntime.h)("strong", null, "Sample Rate Estimate Notice:"), " ", meth.optional_cost_estimate_label)));
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
  const xFor = idx => points.length <= 1 ? padL : padL + idx / (points.length - 1) * plotW;
  const yFor = pct => padT + plotH - Math.max(0, Math.min(100, pct)) / 100 * plotH;
  const utilPath = points.map((p, i) => `${i === 0 ? 'M' : 'L'} ${xFor(i).toFixed(1)} ${yFor(p.mean_gpu_util_percent).toFixed(1)}`).join(' ');
  const fbPath = points.map((p, i) => `${i === 0 ? 'M' : 'L'} ${xFor(i).toFixed(1)} ${yFor(p.mean_fb_used_percent).toFixed(1)}`).join(' ');
  const threshY = yFor(threshold);
  return (0, _jsxRuntime.h)("div", {
    style: {
      overflowX: 'auto'
    }
  }, (0, _jsxRuntime.h)("svg", {
    viewBox: `0 0 ${width} ${height}`,
    style: {
      width: '100%',
      maxHeight: '230px'
    }
  }, [0, 25, 50, 75, 100].map(v => {
    const y = yFor(v);
    return (0, _jsxRuntime.h)("g", {
      key: v
    }, (0, _jsxRuntime.h)("line", {
      x1: padL,
      y1: y,
      x2: width - padR,
      y2: y,
      stroke: "#1e293b",
      strokeWidth: "1"
    }), (0, _jsxRuntime.h)("text", {
      x: padL - 8,
      y: y + 4,
      textAnchor: "end",
      fill: "#64748b",
      fontSize: "10"
    }, v, "%"));
  }), (0, _jsxRuntime.h)("line", {
    x1: padL,
    y1: threshY,
    x2: width - padR,
    y2: threshY,
    stroke: "#f59e0b",
    strokeDasharray: "4 4",
    strokeWidth: "1.2"
  }), (0, _jsxRuntime.h)("text", {
    x: width - padR - 4,
    y: threshY - 5,
    textAnchor: "end",
    fill: "#f59e0b",
    fontSize: "10"
  }, "Low-Util Review Threshold (", threshold, "%)"), (0, _jsxRuntime.h)("path", {
    d: fbPath,
    fill: "none",
    stroke: "#10b981",
    strokeWidth: "2.2"
  }), (0, _jsxRuntime.h)("path", {
    d: utilPath,
    fill: "none",
    stroke: "#38bdf8",
    strokeWidth: "2.5"
  }), points.map((p, i) => (0, _jsxRuntime.h)("g", {
    key: i
  }, (0, _jsxRuntime.h)("circle", {
    cx: xFor(i),
    cy: yFor(p.mean_gpu_util_percent),
    r: "3.2",
    fill: "#38bdf8"
  }), (0, _jsxRuntime.h)("circle", {
    cx: xFor(i),
    cy: yFor(p.mean_fb_used_percent),
    r: "2.8",
    fill: "#10b981"
  }), (i === 0 || i === points.length - 1 || i % 2 === 0) && (0, _jsxRuntime.h)("text", {
    x: xFor(i),
    y: height - 10,
    textAnchor: "middle",
    fill: "#94a3b8",
    fontSize: "10"
  }, p.timestamp.slice(11, 16), "Z")))));
}
function renderSourcesTab(overview, sourcesDetail) {
  const sources = overview.sources || [];
  const mappings = sourcesDetail?.metric_mappings || {};
  const exported = sourcesDetail?.exported_metrics || {};
  return (0, _jsxRuntime.h)("div", null, (0, _jsxRuntime.h)("div", {
    className: "panel"
  }, (0, _jsxRuntime.h)("div", {
    className: "panel-header"
  }, (0, _jsxRuntime.h)("div", {
    className: "panel-title"
  }, "Configured Telemetry & Cluster State Sources"), (0, _jsxRuntime.h)("span", {
    className: "badge healthy"
  }, "Read-Only Transport Enforced")), (0, _jsxRuntime.h)("table", {
    className: "data-table"
  }, (0, _jsxRuntime.h)("thead", null, (0, _jsxRuntime.h)("tr", null, (0, _jsxRuntime.h)("th", null, "Source Name & ID"), (0, _jsxRuntime.h)("th", null, "Kind / Mode"), (0, _jsxRuntime.h)("th", null, "Endpoint"), (0, _jsxRuntime.h)("th", null, "Availability & Freshness"), (0, _jsxRuntime.h)("th", null, "Exported Metrics"), (0, _jsxRuntime.h)("th", null, "Notes & Errors"))), (0, _jsxRuntime.h)("tbody", null, sources.map(s => (0, _jsxRuntime.h)("tr", {
    key: s.id
  }, (0, _jsxRuntime.h)("td", null, (0, _jsxRuntime.h)("div", {
    style: {
      fontWeight: 700
    }
  }, s.name), (0, _jsxRuntime.h)("div", {
    className: "mono",
    style: {
      color: 'var(--text-secondary)',
      fontSize: '11.5px'
    }
  }, s.id)), (0, _jsxRuntime.h)("td", null, (0, _jsxRuntime.h)("span", {
    className: "badge info"
  }, s.kind), ' ', (0, _jsxRuntime.h)("span", {
    className: "badge contextual"
  }, s.mode)), (0, _jsxRuntime.h)("td", {
    className: "mono"
  }, s.endpoint), (0, _jsxRuntime.h)("td", null, (0, _jsxRuntime.h)("span", {
    className: `badge ${s.available && !s.is_stale ? 'healthy' : 'critical'}`
  }, s.available && !s.is_stale ? 'Available & Fresh' : 'Stale / Unavailable'), (0, _jsxRuntime.h)("div", {
    className: "mono",
    style: {
      fontSize: '11.5px',
      marginTop: '4px',
      color: 'var(--text-secondary)'
    }
  }, "Last success: ", s.last_success_at, " (", s.staleness_seconds, "s ago)")), (0, _jsxRuntime.h)("td", {
    className: "mono"
  }, (0, _jsxRuntime.h)("div", null, "Required: ", s.exported_required_count), (0, _jsxRuntime.h)("div", null, "Optional: ", s.exported_optional_count), (s.missing_optional || []).length > 0 && (0, _jsxRuntime.h)("div", {
    style: {
      fontSize: '11px',
      color: 'var(--text-secondary)'
    }
  }, "Unexported optional: ", (s.missing_optional || []).join(', '))), (0, _jsxRuntime.h)("td", {
    style: {
      maxWidth: '340px',
      fontSize: '12px'
    }
  }, s.last_error && (0, _jsxRuntime.h)("div", {
    className: "mono",
    style: {
      color: '#fca5a5',
      marginBottom: '4px'
    }
  }, "Error: ", s.last_error), (0, _jsxRuntime.h)("div", {
    style: {
      color: 'var(--text-secondary)'
    }
  }, s.notes))))))), (0, _jsxRuntime.h)("div", {
    className: "panel"
  }, (0, _jsxRuntime.h)("div", {
    className: "panel-header"
  }, (0, _jsxRuntime.h)("div", {
    className: "panel-title"
  }, "Configurable DCGM Exporter Metric & Label Mappings")), (0, _jsxRuntime.h)("div", {
    className: "section-grid-2"
  }, (0, _jsxRuntime.h)("div", null, (0, _jsxRuntime.h)("h4", {
    style: {
      marginBottom: '8px',
      color: '#7dd3fc'
    }
  }, "Active Metric Field Mappings"), (0, _jsxRuntime.h)("table", {
    className: "data-table"
  }, (0, _jsxRuntime.h)("thead", null, (0, _jsxRuntime.h)("tr", null, (0, _jsxRuntime.h)("th", null, "Canonical Signal"), (0, _jsxRuntime.h)("th", null, "Configured Prometheus / DCGM Name"), (0, _jsxRuntime.h)("th", null, "Exported in Snapshot"))), (0, _jsxRuntime.h)("tbody", null, Object.entries(mappings).filter(([k]) => k.endsWith('_metric')).map(([k, v]) => {
    const metricName = String(v);
    const isPresent = Boolean(exported[metricName]);
    return (0, _jsxRuntime.h)("tr", {
      key: k
    }, (0, _jsxRuntime.h)("td", {
      className: "mono"
    }, k), (0, _jsxRuntime.h)("td", {
      className: "mono"
    }, metricName), (0, _jsxRuntime.h)("td", null, (0, _jsxRuntime.h)("span", {
      className: `badge ${isPresent ? 'healthy' : 'info'}`
    }, isPresent ? 'Exported' : 'Optional / Not Exported')));
  })))), (0, _jsxRuntime.h)("div", null, (0, _jsxRuntime.h)("h4", {
    style: {
      marginBottom: '8px',
      color: '#7dd3fc'
    }
  }, "Label Resolution Priority"), (0, _jsxRuntime.h)("p", {
    style: {
      fontSize: '12.5px',
      color: 'var(--text-secondary)',
      marginBottom: '10px'
    }
  }, "Supports both standard ", (0, _jsxRuntime.h)("span", {
    className: "mono"
  }, "dcgm-exporter --kubernetes"), " labels and legacy", ' ', (0, _jsxRuntime.h)("span", {
    className: "mono"
  }, "--use-old-namespace"), " conventions:"), (0, _jsxRuntime.h)("ul", {
    className: "bullet-list mono",
    style: {
      fontSize: '12px'
    }
  }, (0, _jsxRuntime.h)("li", null, "GPU UUID Labels: ", (mappings.uuid_label_candidates || []).join(', ')), (0, _jsxRuntime.h)("li", null, "Node Hostname Labels: ", (mappings.node_name_label_candidates || []).join(', ')), (0, _jsxRuntime.h)("li", null, "Pod Name Labels: ", (mappings.pod_name_label_candidates || []).join(', ')), (0, _jsxRuntime.h)("li", null, "Pod Namespace Labels: ", (mappings.namespace_label_candidates || []).join(', ')), (0, _jsxRuntime.h)("li", null, "Container Name Labels: ", (mappings.container_label_candidates || []).join(', ')))))));
}
function renderRulesTab(rulesList) {
  return (0, _jsxRuntime.h)("div", null, rulesList.map(r => (0, _jsxRuntime.h)("div", {
    key: r.id,
    className: "panel"
  }, (0, _jsxRuntime.h)("div", {
    className: "panel-header"
  }, (0, _jsxRuntime.h)("div", {
    style: {
      display: 'flex',
      alignItems: 'center',
      gap: '10px'
    }
  }, (0, _jsxRuntime.h)("span", {
    className: "mono",
    style: {
      fontWeight: 800,
      fontSize: '15px',
      color: '#7dd3fc'
    }
  }, r.id), (0, _jsxRuntime.h)("span", {
    className: "panel-title"
  }, r.name), (0, _jsxRuntime.h)("span", {
    className: `badge ${r.default_severity}`
  }, "Default: ", r.default_severity), (0, _jsxRuntime.h)("span", {
    className: "badge info"
  }, r.category))), (0, _jsxRuntime.h)("p", {
    style: {
      marginBottom: '12px',
      color: '#e2e8f0'
    }
  }, r.description), (0, _jsxRuntime.h)("div", {
    className: "section-grid-2"
  }, (0, _jsxRuntime.h)("div", null, (0, _jsxRuntime.h)("h4", {
    style: {
      fontSize: '12.5px',
      color: '#93c5fd',
      marginBottom: '6px'
    }
  }, "Configurable Thresholds & Observation Windows"), (0, _jsxRuntime.h)("table", {
    className: "data-table"
  }, (0, _jsxRuntime.h)("thead", null, (0, _jsxRuntime.h)("tr", null, (0, _jsxRuntime.h)("th", null, "Parameter"), (0, _jsxRuntime.h)("th", null, "Active Value"), (0, _jsxRuntime.h)("th", null, "Purpose"))), (0, _jsxRuntime.h)("tbody", null, (r.thresholds || []).map(t => (0, _jsxRuntime.h)("tr", {
    key: t.key
  }, (0, _jsxRuntime.h)("td", {
    className: "mono"
  }, t.key), (0, _jsxRuntime.h)("td", {
    className: "mono",
    style: {
      fontWeight: 700
    }
  }, t.value, " ", t.unit), (0, _jsxRuntime.h)("td", {
    style: {
      fontSize: '12px',
      color: 'var(--text-secondary)'
    }
  }, t.description)))))), (0, _jsxRuntime.h)("div", null, (0, _jsxRuntime.h)("h4", {
    style: {
      fontSize: '12.5px',
      color: '#93c5fd',
      marginBottom: '6px'
    }
  }, "Required & Optional Input Signals"), (0, _jsxRuntime.h)("div", {
    style: {
      marginBottom: '8px',
      fontSize: '12.5px'
    }
  }, (0, _jsxRuntime.h)("strong", null, "Required:"), ' ', (0, _jsxRuntime.h)("span", {
    className: "mono"
  }, (r.required_signals || []).join(', '))), (r.optional_signals || []).length > 0 && (0, _jsxRuntime.h)("div", {
    style: {
      marginBottom: '8px',
      fontSize: '12.5px'
    }
  }, (0, _jsxRuntime.h)("strong", null, "Optional (used when exported):"), ' ', (0, _jsxRuntime.h)("span", {
    className: "mono"
  }, (r.optional_signals || []).join(', '))), (0, _jsxRuntime.h)("h4", {
    style: {
      fontSize: '12.5px',
      color: '#fcd34d',
      marginTop: '10px',
      marginBottom: '4px'
    }
  }, "Suppression & Inconclusive Safeguards"), (0, _jsxRuntime.h)("ul", {
    className: "bullet-list",
    style: {
      fontSize: '12px',
      color: 'var(--text-secondary)'
    }
  }, (r.suppression_conditions || []).map((c, idx) => (0, _jsxRuntime.h)("li", {
    key: `sup-${idx}`
  }, (0, _jsxRuntime.h)("strong", null, "Suppressed when:"), " ", c)), (r.inconclusive_conditions || []).map((c, idx) => (0, _jsxRuntime.h)("li", {
    key: `inc-${idx}`
  }, (0, _jsxRuntime.h)("strong", null, "Inconclusive when:"), " ", c))))))));
}
  };
  __modules["./main"] = function(module, exports, require) {
"use strict";

var _jsxRuntime = require("./jsx-runtime");
var _App = require("./App");
const rootEl = document.getElementById('root');
if (rootEl) {
  (0, _jsxRuntime.mountApp)(() => (0, _jsxRuntime.h)(_App.App, null), rootEl);
}
  };
  const rt = __require("./jsx-runtime");
  window.h = rt.h;
  window.Fragment = rt.Fragment;
  __require("./main");
})();
