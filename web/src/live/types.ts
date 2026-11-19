// Shapes of GET /api/v1/live/state (see internal/live/types.go). Deeply shared domain objects stay loosely typed.
import { Incident } from '../types';

export type SourceState = 'pending' | 'connected' | 'degraded' | 'unreachable' | 'not_found' | 'disabled' | 'error';
export type CheckStatus = 'pass' | 'warn' | 'fail' | 'skip' | 'info' | 'pending';

export interface LiveStatus {
  runtime: string;
  version: string;
  supported: boolean;
  unsupported_reason: string;
  state: 'idle' | 'running';
  state_reason: string;
  started_at: string | null;
  tick: number;
  interval_seconds: number;
  last_tick_at: string | null;
  last_tick_duration_ms: number;
  server_time: string;
  config_mutable: boolean;
  config_mutable_reason: string;
  auto_start: boolean;
  hostname: string;
  emulated: boolean;
  // Access control (0.3+). Without authentication every caller is "admin".
  role?: 'admin' | 'viewer';
  auth_enabled?: boolean;
  user?: string;
  can_operate?: boolean;
}

export interface LiveSourceConfig {
  enabled: boolean;
  explicit: boolean;
  endpoint: string;
  token_set: boolean;
  token_file: string;
  ca_file: string;
}

export interface LiveConfigView {
  enabled: boolean;
  auto_start: boolean;
  allow_remote_config: boolean;
  interval_seconds: number;
  idle_timeout_seconds: number;
  history_points: number;
  cluster_name: string;
  nvidia_smi_enabled: boolean;
  nvidia_smi_path: string;
  amd_smi_enabled?: boolean;
  amd_smi_path?: string;
  host_vitals_enabled: boolean;
  dcgm_exporter: LiveSourceConfig;
  prometheus: LiveSourceConfig;
  kubernetes: LiveSourceConfig;
  prometheus_backfill_minutes: number;
  in_cluster: boolean;
}

export interface Fact {
  label: string;
  value: string;
}

export interface LiveSource {
  id: string;
  name: string;
  kind: string;
  enabled: boolean;
  explicit: boolean;
  state: SourceState;
  endpoint: string;
  detail: string;
  hint: string;
  last_attempt_at: string | null;
  last_success_at: string | null;
  last_error: string;
  last_latency_ms: number | null;
  latency_history_ms: number[];
  consecutive_failures: number;
  success_count: number;
  failure_count: number;
  refresh_seconds: number;
  emulated: boolean;
  read_only: string;
  facts: Fact[];
}

export interface Check {
  id: string;
  title: string;
  status: CheckStatus;
  detail: string;
  hint: string;
  latency_ms: number | null;
}

export interface CheckGroup {
  id: string;
  title: string;
  checks: Check[];
}

export interface Preflight {
  generated_at: string;
  verdict: 'idle' | 'checking' | 'blocked' | 'degraded' | 'ready';
  verdict_text: string;
  summary: Record<string, number>;
  groups: CheckGroup[];
}

export interface GPUMetricPoint {
  timestamp: string;
  gpu_util_percent: number | null;
  mem_copy_util_percent: number | null;
  fb_used_mib: number | null;
  fb_free_mib: number | null;
  gpu_temp_celsius: number | null;
  memory_temp_celsius: number | null;
  power_usage_watts: number | null;
  xid_error_code: number | null;
  pcie_replay_counter: number | null;
  row_remap_failure: number | null;
  uncorrectable_remapped_rows: number | null;
  ecc_dbe_vol_total: number | null;
  ecc_sbe_vol_total: number | null;
  thermal_violation_ns: number | null;
  power_violation_ns: number | null;
}

export interface LiveGPU {
  uuid: string;
  device_index: number;
  node_name: string;
  model_name: string;
  pci_bus_id: string;
  driver_version: string;
  power_limit_watts: number;
  total_memory_mib: number;
  allocated_namespace: string;
  allocated_pod: string;
  allocated_container: string;
  mapping_status: string;
  last_sample_at: string;
  staleness_seconds: number;
  is_stale: boolean;
  latest: GPUMetricPoint;
  fb_used_percent: number | null;
  sources: string[];
  fan_speed_percent: number | null;
  pstate: string;
  throttle_reasons: string[];
  pcie: { gen_current: number; gen_max: number; width_current: number; width_max: number } | null;
  processes: { pid: number; name: string; used_memory_mib: number | null }[];
  emulated: boolean;
  mig_profile: string;
  sample_count: number;
  first_seen_at: string;
  incident_ids: string[];
  worst_severity: string;
  exported_metrics?: string[];
}

export interface HistoryPoint {
  seq: number;
  t: string;
  gpus: number;
  fresh_gpus: number;
  util: number | null;
  fb: number | null;
  temp_max: number | null;
  power: number | null;
  crit: number;
  warn: number;
  cpu: number | null;
  mem: number | null;
  backfilled?: boolean;
  // per GPU: [util %, VRAM %, temp C, power W]
  g: Record<string, number[]>;
}

export interface ActivityEntry {
  seq: number;
  t: string;
  severity: string;
  kind: string;
  title: string;
  detail: string;
}

export interface ResolvedIncident {
  incident: Incident;
  resolved_at: string;
  duration_seconds: number;
}

export interface HostVitals {
  supported: boolean;
  collected_at: string;
  hostname: string;
  os: string;
  arch: string;
  platform: string;
  kernel: string;
  cpu_model: string;
  logical_cpus: number;
  in_container: boolean;
  uptime_seconds: number | null;
  cpu_percent: number | null;
  cpu_iowait_percent: number | null;
  load1: number | null;
  load5: number | null;
  load15: number | null;
  mem_total_mib: number | null;
  mem_available_mib: number | null;
  mem_used_percent: number | null;
  swap_total_mib: number | null;
  swap_used_mib: number | null;
  disk_path: string;
  disk_total_gib: number | null;
  disk_used_percent: number | null;
  net_rx_bytes_per_sec: number | null;
  net_tx_bytes_per_sec: number | null;
  pressure: { cpu_some_avg10: number | null; memory_some_avg10: number | null; io_some_avg10: number | null } | null;
  process: { pid: number; uptime_seconds: number; memory_mib: number; workers: number; runtime: string };
  pci_gpus: { address: string; vendor: string; vendor_id: string; device_id: string; class: string; driver: string }[];
  pci_scan_supported: boolean;
  nvidia_driver_version: string;
  notes: string[];
}

export interface LiveState {
  status: LiveStatus;
  config: LiveConfigView;
  overview: any | null;
  preflight: Preflight;
  sources: LiveSource[];
  host: HostVitals | null;
  gpus: LiveGPU[];
  processes: { gpu_uuid: string; gpu_index: number; node_name: string; pid: number; name: string; used_memory_mib: number | null }[];
  nodes: any[];
  pods: any[];
  events: any[];
  history: { epoch: number; last_seq: number; first_seq: number; capacity: number; points: HistoryPoint[] };
  activity: ActivityEntry[];
  activity_last_seq: number;
  resolved_incidents: ResolvedIncident[];
  thresholds: Record<string, number>;
  threshold_defaults: Record<string, number>;
  threshold_overrides: string[];
  rules: any[];
  sources_detail: any | null;
  notes: string[];
  alerts?: AlertStatus;
  persistence?: PersistenceStatus;
}

export interface AlertDestination {
  kind: 'slack' | 'webhook';
  target: string;
  signed: boolean;
  sent: number;
  failed: number;
  dropped: number;
  last_event: string;
  last_attempt_at: string | null;
  last_success_at: string | null;
  last_error: string;
}

export interface AlertStatus {
  enabled: boolean;
  min_severity: string;
  public_url: string;
  tracked_incidents: number;
  destinations: AlertDestination[];
  note: string;
}

export interface PersistenceStatus {
  enabled: boolean;
  path: string;
  loaded_at: string | null;
  saved_at: string | null;
  last_error: string;
  note: string;
}

export interface FleetSummary {
  cluster: string;
  hostname: string;
  version: string;
  runtime: string;
  state: string;
  verdict: string;
  verdict_text: string;
  fleet_status: string;
  data_mode: string;
  emulated: boolean;
  gpus_total: number;
  gpus_fresh: number;
  nodes: number;
  incidents: Record<string, number>;
  top_incidents: string[];
  sources_connected: number;
  sources_enabled: number;
  last_cycle_at: string | null;
}

export interface FleetMember {
  name: string;
  kind: 'local' | 'peer';
  url: string;
  reachable: boolean;
  error: string;
  last_ok_at: string | null;
  latency_ms: number;
  summary: FleetSummary | null;
}

export interface FleetView {
  enabled: boolean;
  interval_seconds: number;
  members: FleetMember[];
  totals: { members: number; reachable: number; gpus_total: number; gpus_fresh: number; critical: number; warning: number; info: number; unhealthy: number; sources_connected: number };
  note: string;
}
