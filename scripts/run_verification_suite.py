#!/usr/bin/env python3
"""
Automated Verification & Test Suite for GPU Lens.
Executes unit and integration tests covering:
1. All 6 deterministic diagnostic rules (firing when evidence is present, not firing when absent).
2. Missing or stale data policy (never producing a healthy status by default).
3. Low-utilization safeguard (never describing low utilization as proven waste; surfacing VRAM context).
4. Unsupported optional metrics safeguard (never fabricating findings).
5. Incident fingerprint deduplication across repeated evaluations in SQLite.
6. Offline demo reproducibility across all 6 bundled scenarios without API credentials.
7. Live HTTP API server integration test across all endpoints (/healthz, /readyz, /api/v1/overview, /api/v1/incidents, /api/v1/capacity, /api/v1/sources, /api/v1/rules, /api/v1/scenarios).
8. Go codebase structural & package completeness audit.
"""
import json
import os
import sys
import threading
import unittest
import urllib.request
import urllib.error
import http.server

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from local_runtime_server import (
    DEFAULT_THRESHOLDS,
    GPULensAppState,
    SQLiteIncidentStore,
    build_capacity_report,
    compute_fingerprint,
    evaluate_rules,
    make_handler,
)

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FIXTURES_DIR = os.path.join(REPO_ROOT, "fixtures")
STATIC_DIR = os.path.join(REPO_ROOT, "web", "dist")


class TestGPULensDiagnosticSuite(unittest.TestCase):
    def setUp(self):
        self.state = GPULensAppState(FIXTURES_DIR, "scenario-1-healthy", ":memory:", STATIC_DIR)

    def test_01_healthy_cluster_produces_zero_incidents(self):
        ov = self.state.build_overview("scenario-1-healthy")
        self.assertEqual(ov["fleet_health_status"], "healthy")
        self.assertEqual(len(ov["active_incidents"]), 0)
        self.assertEqual(ov["capacity_summary"]["total_capacity_gpus"], 8)
        self.assertEqual(ov["capacity_summary"]["total_allocatable_gpus"], 8)
        self.assertEqual(ov["capacity_summary"]["total_allocated_gpus"], 6)
        self.assertEqual(ov["capacity_summary"]["total_free_allocatable_gpus"], 2)

    def test_02_pending_capacity_and_fragmentation_rule_fires_with_evidence(self):
        ov = self.state.build_overview("scenario-2-pending-capacity")
        incidents = ov["active_incidents"]
        self.assertEqual(len(incidents), 2)
        rule_ids = {i["rule_id"] for i in incidents}
        self.assertEqual(rule_ids, {"GPU-SCHED-001"})
        summaries = " ".join(i["summary"] for i in incidents)
        facts = " ".join(" ".join(i["observed_facts"]) for i in incidents)
        self.assertIn("Insufficient aggregate allocatable GPUs", summaries)
        self.assertIn("Node selector / affinity constraint", summaries)
        self.assertIn("nodeSelector", facts)

    def test_03_pending_pod_without_evidence_marks_inconclusive_never_guesses(self):
        custom_snap = {
            "scenario_id": "test-inconclusive-sched",
            "cluster_name": "test-cluster",
            "data_mode": "simulated_fixture",
            "collected_at": "2026-10-18T12:00:00Z",
            "nodes": [],
            "gpus": [],
            "pods": [
                {
                    "name": "mystery-pending-0",
                    "namespace": "ml",
                    "phase": "Pending",
                    "requested_gpus": 4,
                    "created_at": "2026-10-18T11:30:00Z",
                }
            ],
            "events": [],
            "sources": [],
        }
        incidents = evaluate_rules(custom_snap, DEFAULT_THRESHOLDS)
        self.assertEqual(len(incidents), 1)
        self.assertEqual(incidents[0]["eval_status"], "inconclusive")
        self.assertEqual(incidents[0]["evidence_strength"], "inconclusive")
        self.assertIn("Refusing to guess", incidents[0]["summary"])

    def test_04_gpu_health_and_thermal_rules_fire_on_xid_and_row_remap(self):
        ov = self.state.build_overview("scenario-3-gpu-health-warning")
        incidents = ov["active_incidents"]
        rule_ids = {i["rule_id"] for i in incidents}
        self.assertIn("GPU-HEALTH-002", rule_ids)
        self.assertIn("GPU-THERM-006", rule_ids)
        health_inc = next(i for i in incidents if i["rule_id"] == "GPU-HEALTH-002")
        self.assertEqual(health_inc["severity"], "critical")
        self.assertEqual(health_inc["evidence_strength"], "strong")
        self.assertEqual(ov["capacity_summary"]["unavailable_hardware_gpus"], 1)

    def test_05_low_utilization_never_claims_proven_waste_and_surfaces_vram(self):
        ov = self.state.build_overview("scenario-4-low-utilization")
        incidents = ov["active_incidents"]
        self.assertEqual(len(incidents), 2)
        for inc in incidents:
            self.assertEqual(inc["rule_id"], "GPU-UTIL-003")
            self.assertEqual(inc["evidence_strength"], "contextual")
            self.assertGreaterEqual(len(inc["suggested_checks"]), 3)
            self.assertNotIn("proven waste", inc["summary"].lower())
        vram_inc = next(i for i in incidents if "llama3-70b-int8-realtime-0" in i["title"])
        self.assertTrue(any("High GPU framebuffer memory residency" in ev["summary"] for ev in vram_inc["contradicting_evidence"]))
        cap = ov["capacity_summary"]
        self.assertEqual(cap["observed_low_util_gpu_hours"], 6.0)
        self.assertIn("NOT actual cloud billing or realized savings", cap["methodology"]["optional_cost_estimate_label"])

    def test_06_failure_correlation_distinguishes_xid79_from_userspace_oom(self):
        ov = self.state.build_overview("scenario-5-correlated-failure")
        incidents = ov["active_incidents"]
        corr_incs = [i for i in incidents if i["rule_id"] == "GPU-CORR-004"]
        self.assertEqual(len(corr_incs), 2)
        hw_inc = next(i for i in corr_incs if "multimodal-train-worker-0" in i["title"])
        oom_inc = next(i for i in corr_incs if "dataloader-oom-job-0" in i["title"])
        self.assertEqual(hw_inc["severity"], "critical")
        self.assertEqual(hw_inc["evidence_strength"], "strong")
        self.assertEqual(oom_inc["severity"], "warning")
        self.assertTrue(any(ev["polarity"] == "contradicting" for ev in oom_inc["contradicting_evidence"]))

    def test_07_stale_and_unmapped_telemetry_triggers_blind_spot_never_healthy(self):
        ov = self.state.build_overview("scenario-6-stale-telemetry")
        self.assertEqual(ov["fleet_health_status"], "telemetry_blind_spot")
        incidents = ov["active_incidents"]
        fresh_incs = [i for i in incidents if i["rule_id"] == "GPU-FRESH-005"]
        self.assertGreaterEqual(len(fresh_incs), 2)
        corr_inc = next(i for i in incidents if i["rule_id"] == "GPU-CORR-004")
        self.assertEqual(corr_inc["eval_status"], "inconclusive")
        cap = ov["capacity_summary"]
        self.assertFalse(cap["per_job_attribution_complete"])
        self.assertEqual(len(cap["low_utilization_workloads"]), 0)

    def test_08_unsupported_optional_metrics_do_not_fabricate_findings(self):
        snap = {
            "scenario_id": "test-no-optional-metrics",
            "cluster_name": "test-cluster",
            "data_mode": "simulated_fixture",
            "collected_at": "2026-10-18T12:00:00Z",
            "nodes": [{"name": "n1", "ready": True, "schedulable": True, "gpu_capacity": 1, "gpu_allocatable": 1, "gpu_allocated": 1}],
            "gpus": [
                {
                    "uuid": "GPU-MINIMAL-0",
                    "device_index": 0,
                    "node_name": "n1",
                    "model_name": "NVIDIA A100",
                    "mapping_status": "mapped",
                    "last_sample_at": "2026-10-18T11:59:50Z",
                    "staleness_seconds": 10.0,
                    "is_stale": False,
                    "latest": {
                        "timestamp": "2026-10-18T11:59:50Z",
                        "gpu_util_percent": 75.0,
                        "mem_copy_util_percent": 30.0,
                        "fb_used_mib": 20000,
                        "fb_free_mib": 20000,
                        "gpu_temp_celsius": 62.0,
                        "memory_temp_celsius": 65.0,
                        "power_usage_watts": 300.0,
                        "xid_error_code": 0,
                        "pcie_replay_counter": 0,
                        "row_remap_failure": 0,
                    },
                    "samples": [],
                }
            ],
            "pods": [],
            "events": [],
            "sources": [{"id": "s1", "name": "Prom", "available": True, "last_success_at": "2026-10-18T11:59:50Z", "staleness_seconds": 10.0, "is_stale": False}],
        }
        incidents = evaluate_rules(snap, DEFAULT_THRESHOLDS)
        self.assertEqual(len(incidents), 0)

    def test_09_repeated_inputs_deduplicate_by_fingerprint_in_sqlite(self):
        store = SQLiteIncidentStore(":memory:")
        snap = self.state.scenarios["scenario-3-gpu-health-warning"]
        evaluated = evaluate_rules(snap, DEFAULT_THRESHOLDS)
        batch1 = store.upsert_batch("scenario-3", evaluated)
        self.assertEqual(batch1[0]["occurrence_count"], 1)
        batch2 = store.upsert_batch("scenario-3", evaluated)
        self.assertEqual(len(batch2), len(batch1))
        self.assertEqual(batch2[0]["occurrence_count"], 2)

    def test_10_http_api_endpoints_and_scenario_switching(self):
        srv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), make_handler(self.state))
        port = srv.server_address[1]
        t = threading.Thread(target=srv.serve_forever, daemon=True)
        t.start()
        try:
            for path in ["/healthz", "/readyz", "/api/v1/overview", "/api/v1/incidents", "/api/v1/capacity", "/api/v1/sources", "/api/v1/rules", "/api/v1/scenarios"]:
                with urllib.request.urlopen(f"http://127.0.0.1:{port}{path}", timeout=5) as resp:
                    self.assertEqual(resp.status, 200)
                    data = json.loads(resp.read().decode("utf-8"))
                    self.assertIsInstance(data, dict)

            req = urllib.request.Request(
                f"http://127.0.0.1:{port}/api/v1/scenarios/select",
                data=json.dumps({"scenario_id": "scenario-5-correlated-failure"}).encode("utf-8"),
                headers={"Content-Type": "application/json"},
                method="POST",
            )
            with urllib.request.urlopen(req, timeout=5) as resp:
                self.assertEqual(resp.status, 200)
                ov = json.loads(resp.read().decode("utf-8"))
                self.assertEqual(ov["scenario_id"], "scenario-5-correlated-failure")
                self.assertGreaterEqual(len(ov["active_incidents"]), 2)
        finally:
            srv.shutdown()
            srv.server_close()

    def test_11_go_source_files_and_tests_exist_and_have_valid_packages(self):
        expected_go_files = [
            "cmd/gpulens/main.go",
            "internal/domain/models.go",
            "internal/config/config.go",
            "internal/config/config_test.go",
            "internal/rules/engine.go",
            "internal/rules/engine_test.go",
            "internal/capacity/calculator.go",
            "internal/capacity/calculator_test.go",
            "internal/source/readonly_transport.go",
            "internal/source/fixture_source.go",
            "internal/source/kubernetes_source.go",
            "internal/source/dcgm_exporter_source.go",
            "internal/source/prometheus_source.go",
            "internal/source/source_test.go",
            "internal/store/store.go",
            "internal/store/store_test.go",
            "internal/api/server.go",
            "internal/api/server_test.go",
        ]
        for rel in expected_go_files:
            full = os.path.join(REPO_ROOT, rel)
            self.assertTrue(os.path.isfile(full), f"Missing Go file: {rel}")
            with open(full, "r", encoding="utf-8") as f:
                content = f.read()
            self.assertTrue(content.startswith("package "), f"Invalid package header in {rel}")
            self.assertEqual(content.count("{"), content.count("}"), f"Unbalanced braces in {rel}")


if __name__ == "__main__":
    unittest.main(verbosity=2)
