// Strict TypeScript interfaces matching the Go backend REST API responses (`internal/domain/models.go`).

export type Severity = 'info' | 'warning' | 'critical';
export type EvalStatus = 'triggered' | 'inconclusive' | 'suppressed' | 'healthy';
export type EvidenceStrength = 'strong' | 'moderate' | 'contextual' | 'inconclusive';

export interface ScenarioSummary {
  id: string;
  name: string;
  description: string;
  active: boolean;
}

export interface SourceHealth {
  id: string;
  name: string;
  kind: string;
  mode: string;
  endpoint: string;
  available: boolean;
  read_only_verified: boolean;
  last_success_at: string;
  last_error?: string;
  staleness_seconds: number;
  is_stale: boolean;
  exported_required_count: number;
  exported_optional_count: number;
  missing_optional?: string[];
  missing_required?: string[];
  notes?: string;
}

export interface EvidenceItem {
  kind: string;
  polarity: 'supporting' | 'contradicting' | 'contextual';
  source: string;
  metric_name?: string;
  object_ref: string;
  summary: string;
  value: string;
  threshold?: string;
  observed_at: string;
}

export interface TimelineEntry {
  timestamp: string;
  layer: string;
  severity: string;
  object_ref: string;
  event_type: string;
  summary: string;
}

export interface Incident {
  id: string;
  fingerprint: string;
  rule_id: string;
  rule_name: string;
  category: string;
  severity: Severity;
  eval_status: EvalStatus;
  evidence_strength: EvidenceStrength;
  evidence_strength_basis: string;
  title: string;
  summary: string;
  observed_facts: string[];
  possible_causes: string[];
  supporting_evidence: EvidenceItem[];
  contradicting_evidence: EvidenceItem[];
  timeline: TimelineEntry[];
  affected_resources: string[];
  suggested_checks: string[];
  inconclusive_reason?: string;
  data_mode: string;
  scenario_id: string;
  first_seen_at: string;
  last_seen_at: string;
  occurrence_count: number;
}

export interface NodeCapacityBreakdown {
  node_name: string;
  gpu_model: string;
  schedulable: boolean;
  ready: boolean;
  capacity_gpus: number;
  allocatable_gpus: number;
  unavailable_gpus: number;
  allocated_gpus: number;
  free_allocatable_gpus: number;
  mean_gpu_util_percent?: number;
  mean_fb_used_percent?: number;
  fresh_gpus: number;
  stale_gpus: number;
  unmapped_pod_gpus: number;
  status_note: string;
}

export interface PendingWorkloadSummary {
  namespace: string;
  pod_name: string;
  owner_ref: string;
  requested_gpus: number;
  pending_minutes: number;
  observed_reason: string;
  scheduler_message: string;
  evidence_complete: boolean;
  created_at: string;
}

export interface LowUtilPodWindow {
  namespace: string;
  pod_name: string;
  owner_ref: string;
  node_name: string;
  allocated_gpus: number;
  assigned_gpu_uuids: string[];
  observation_window_minutes: number;
  sample_count: number;
  mean_gpu_util_percent: number;
  min_gpu_util_percent: number;
  max_gpu_util_percent: number;
  mean_mem_copy_util_percent: number;
  mean_fb_used_mib: number;
  mean_fb_used_percent: number;
  total_allocated_gpu_hours: number;
  low_util_gpu_hours: number;
  optional_sample_cost_estimate_usd?: number;
  last_data_update: string;
  workload_context_note: string;
  follow_up_checklist: string[];
}

export interface UtilizationHistoryPoint {
  timestamp: string;
  mean_gpu_util_percent: number;
  mean_mem_copy_util_percent: number;
  mean_fb_used_percent: number;
  allocated_gpus_observed: number;
}

export interface CapacityMethodology {
  util_threshold_percent: number;
  min_window_minutes: number;
  min_samples_required: number;
  formula_summary: string;
  step_by_step_calculation: string[];
  non_waste_disclaimer: string;
  missing_mapping_policy: string;
  optional_sample_hourly_rate_usd?: number;
  optional_cost_estimate_label?: string;
}

export interface CapacityReport {
  cluster_name: string;
  scenario_id: string;
  data_mode: string;
  generated_at: string;
  total_capacity_gpus: number;
  total_allocatable_gpus: number;
  unavailable_hardware_gpus: number;
  total_allocated_gpus: number;
  total_free_allocatable_gpus: number;
  pending_requested_gpus: number;
  cluster_mean_gpu_util_percent?: number;
  cluster_mean_fb_used_percent?: number;
  total_observed_allocated_gpu_hours: number;
  observed_low_util_gpu_hours: number;
  optional_low_util_sample_cost_usd?: number;
  telemetry_fresh_gpus: number;
  telemetry_stale_gpus: number;
  mapped_allocated_gpus: number;
  unmapped_allocated_gpus: number;
  per_job_attribution_complete: boolean;
  attribution_blind_spot_warning?: string;
  nodes: NodeCapacityBreakdown[];
  pending_workloads: PendingWorkloadSummary[];
  low_utilization_workloads: LowUtilPodWindow[];
  historical_utilization: UtilizationHistoryPoint[];
  methodology: CapacityMethodology[];
}

export interface OverviewResponse {
  cluster_name: string;
  scenario_id: string;
  scenario_name: string;
  scenario_description: string;
  data_mode: string;
  collected_at: string;
  fleet_health_status: 'healthy' | 'warning' | 'critical' | 'telemetry_blind_spot';
  fleet_health_headline: string;
  active_incident_counts: Record<string, number>;
  capacity_summary: any;
  active_incidents: Incident[];
  sources: SourceHealth[];
}

export interface RuleThresholdView {
  key: string;
  label: string;
  value: number;
  unit: string;
  description: string;
}

export interface RuleDefinition {
  id: string;
  name: string;
  category: string;
  default_severity: Severity;
  description: string;
  required_signals: string[];
  optional_signals: string[];
  thresholds: RuleThresholdView[];
  suppression_conditions: string[];
  inconclusive_conditions: string[];
  suggested_checks: string[];
}
