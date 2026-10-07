// Generated from contracts/1.0/openapi.json by openapi-typescript 7.13.0. Do not edit.
// Source SHA-256: 0874615ebca4cd17077fe3732f6ede55dd84d82268ecbdbf77b42e2a07af99b3
export const operationRoutes = {
  "get_alarms": {
    "method": "GET",
    "path": "/alarms"
  },
  "get_alarms_id": {
    "method": "GET",
    "path": "/alarms/{id}"
  },
  "post_alarms_id_acknowledge": {
    "method": "POST",
    "path": "/alarms/{id}/acknowledge"
  },
  "post_alarms_id_actions": {
    "method": "POST",
    "path": "/alarms/{id}/actions"
  },
  "get_analysis_runs": {
    "method": "GET",
    "path": "/analysis-runs"
  },
  "post_analysis_runs": {
    "method": "POST",
    "path": "/analysis-runs"
  },
  "get_analysis_runs_id": {
    "method": "GET",
    "path": "/analysis-runs/{id}"
  },
  "get_analysis_runs_id_snapshot": {
    "method": "GET",
    "path": "/analysis-runs/{id}/snapshot"
  },
  "get_analysis_runs_id_steps": {
    "method": "GET",
    "path": "/analysis-runs/{id}/steps"
  },
  "get_asset_proposals": {
    "method": "GET",
    "path": "/asset-proposals"
  },
  "post_asset_proposals_id_decide": {
    "method": "POST",
    "path": "/asset-proposals/{id}/decide"
  },
  "post_assistant": {
    "method": "POST",
    "path": "/assistant"
  },
  "get_audit": {
    "method": "GET",
    "path": "/audit"
  },
  "post_audit_verify": {
    "method": "POST",
    "path": "/audit/verify"
  },
  "get_catalogue": {
    "method": "GET",
    "path": "/catalogue"
  },
  "get_compatibility": {
    "method": "GET",
    "path": "/compatibility"
  },
  "get_config": {
    "method": "GET",
    "path": "/config"
  },
  "post_config": {
    "method": "POST",
    "path": "/config"
  },
  "post_config_id_acknowledge": {
    "method": "POST",
    "path": "/config/{id}/acknowledge"
  },
  "get_configuration_reports": {
    "method": "GET",
    "path": "/configuration-reports"
  },
  "get_configuration_runtime": {
    "method": "GET",
    "path": "/configuration-runtime"
  },
  "get_connector_configurations": {
    "method": "GET",
    "path": "/connector-configurations"
  },
  "post_connector_configurations": {
    "method": "POST",
    "path": "/connector-configurations"
  },
  "get_contracts": {
    "method": "GET",
    "path": "/contracts"
  },
  "get_contracts_name": {
    "method": "GET",
    "path": "/contracts/{name}"
  },
  "get_control_operations_id": {
    "method": "GET",
    "path": "/control-operations/{id}"
  },
  "get_dashboards": {
    "method": "GET",
    "path": "/dashboards"
  },
  "post_dashboards": {
    "method": "POST",
    "path": "/dashboards"
  },
  "get_data": {
    "method": "GET",
    "path": "/data"
  },
  "get_definitions": {
    "method": "GET",
    "path": "/definitions"
  },
  "post_definitions_id_deactivate": {
    "method": "POST",
    "path": "/definitions/{id}/deactivate"
  },
  "get_definitions_id_plans": {
    "method": "GET",
    "path": "/definitions/{id}/plans"
  },
  "post_definitions_id_rollback": {
    "method": "POST",
    "path": "/definitions/{id}/rollback"
  },
  "get_definitions_id_versions": {
    "method": "GET",
    "path": "/definitions/{id}/versions"
  },
  "post_device_configurations": {
    "method": "POST",
    "path": "/device-configurations"
  },
  "post_device_configurations_validate": {
    "method": "POST",
    "path": "/device-configurations/validate"
  },
  "get_device_protocols": {
    "method": "GET",
    "path": "/device-protocols"
  },
  "get_devices_id_configuration": {
    "method": "GET",
    "path": "/devices/{id}/configuration"
  },
  "get_drafts": {
    "method": "GET",
    "path": "/drafts"
  },
  "post_drafts": {
    "method": "POST",
    "path": "/drafts"
  },
  "get_drafts_id_diff": {
    "method": "GET",
    "path": "/drafts/{id}/diff"
  },
  "post_drafts_id_impact": {
    "method": "POST",
    "path": "/drafts/{id}/impact"
  },
  "post_drafts_id_publish": {
    "method": "POST",
    "path": "/drafts/{id}/publish"
  },
  "post_drafts_id_semantic_diff": {
    "method": "POST",
    "path": "/drafts/{id}/semantic-diff"
  },
  "post_drafts_id_simulate": {
    "method": "POST",
    "path": "/drafts/{id}/simulate"
  },
  "post_drafts_id_validate": {
    "method": "POST",
    "path": "/drafts/{id}/validate"
  },
  "get_entities": {
    "method": "GET",
    "path": "/entities"
  },
  "post_entities": {
    "method": "POST",
    "path": "/entities"
  },
  "get_events": {
    "method": "GET",
    "path": "/events"
  },
  "get_executions": {
    "method": "GET",
    "path": "/executions"
  },
  "post_executions": {
    "method": "POST",
    "path": "/executions"
  },
  "get_executions_id": {
    "method": "GET",
    "path": "/executions/{id}"
  },
  "post_executions_id_approve": {
    "method": "POST",
    "path": "/executions/{id}/approve"
  },
  "post_executions_id_cancel": {
    "method": "POST",
    "path": "/executions/{id}/cancel"
  },
  "post_executions_id_dispatch": {
    "method": "POST",
    "path": "/executions/{id}/dispatch"
  },
  "post_executions_id_reconcile": {
    "method": "POST",
    "path": "/executions/{id}/reconcile"
  },
  "post_executions_id_resume": {
    "method": "POST",
    "path": "/executions/{id}/resume"
  },
  "post_grants": {
    "method": "POST",
    "path": "/grants"
  },
  "post_ingest": {
    "method": "POST",
    "path": "/ingest"
  },
  "get_investigations": {
    "method": "GET",
    "path": "/investigations"
  },
  "delete_investigations_id": {
    "method": "DELETE",
    "path": "/investigations/{id}"
  },
  "get_investigations_id": {
    "method": "GET",
    "path": "/investigations/{id}"
  },
  "get_investigations_id_evidence_evidence_id": {
    "method": "GET",
    "path": "/investigations/{id}/evidence/{evidence_id}"
  },
  "get_jobs": {
    "method": "GET",
    "path": "/jobs"
  },
  "post_jobs": {
    "method": "POST",
    "path": "/jobs"
  },
  "post_jobs_id_retry": {
    "method": "POST",
    "path": "/jobs/{id}/retry"
  },
  "post_login": {
    "method": "POST",
    "path": "/login"
  },
  "post_logout": {
    "method": "POST",
    "path": "/logout"
  },
  "get_me": {
    "method": "GET",
    "path": "/me"
  },
  "get_notifications": {
    "method": "GET",
    "path": "/notifications"
  },
  "get_organization": {
    "method": "GET",
    "path": "/organization"
  },
  "post_organization_sync": {
    "method": "POST",
    "path": "/organization/sync"
  },
  "get_overview": {
    "method": "GET",
    "path": "/overview"
  },
  "get_plugins": {
    "method": "GET",
    "path": "/plugins"
  },
  "post_plugins": {
    "method": "POST",
    "path": "/plugins"
  },
  "post_queries_kind": {
    "method": "POST",
    "path": "/queries/{kind}"
  },
  "post_queries_kind_events": {
    "method": "POST",
    "path": "/queries/{kind}/events"
  },
  "get_release_artifacts": {
    "method": "GET",
    "path": "/release-artifacts"
  },
  "post_release_artifacts": {
    "method": "POST",
    "path": "/release-artifacts"
  },
  "put_release_artifacts_sha256": {
    "method": "PUT",
    "path": "/release-artifacts/{sha256}"
  },
  "get_release_deployments": {
    "method": "GET",
    "path": "/release-deployments"
  },
  "post_release_deployments": {
    "method": "POST",
    "path": "/release-deployments"
  },
  "get_release_deployments_id": {
    "method": "GET",
    "path": "/release-deployments/{id}"
  },
  "post_release_deployments_id_actions": {
    "method": "POST",
    "path": "/release-deployments/{id}/actions"
  },
  "get_release_deployments_id_reports": {
    "method": "GET",
    "path": "/release-deployments/{id}/reports"
  },
  "post_release_deployments_id_rollback": {
    "method": "POST",
    "path": "/release-deployments/{id}/rollback"
  },
  "get_releases": {
    "method": "GET",
    "path": "/releases"
  },
  "post_releases": {
    "method": "POST",
    "path": "/releases"
  },
  "get_releases_id": {
    "method": "GET",
    "path": "/releases/{id}"
  },
  "post_releases_validate": {
    "method": "POST",
    "path": "/releases/validate"
  },
  "get_rule_nodes": {
    "method": "GET",
    "path": "/rule-nodes"
  },
  "get_runtime": {
    "method": "GET",
    "path": "/runtime"
  },
  "get_scene_templates": {
    "method": "GET",
    "path": "/scene-templates"
  },
  "get_shadow_candidates": {
    "method": "GET",
    "path": "/shadow-candidates"
  },
  "post_shadow_candidates": {
    "method": "POST",
    "path": "/shadow-candidates"
  },
  "get_shadow_candidates_id": {
    "method": "GET",
    "path": "/shadow-candidates/{id}"
  },
  "post_shadow_candidates_id_stop": {
    "method": "POST",
    "path": "/shadow-candidates/{id}/stop"
  },
  "get_task_queues": {
    "method": "GET",
    "path": "/task-queues"
  },
  "get_tasks": {
    "method": "GET",
    "path": "/tasks"
  },
  "get_tasks_id": {
    "method": "GET",
    "path": "/tasks/{id}"
  },
  "post_tasks_id_cancel": {
    "method": "POST",
    "path": "/tasks/{id}/cancel"
  },
  "post_tasks_id_retry": {
    "method": "POST",
    "path": "/tasks/{id}/retry"
  },
  "post_template_batches": {
    "method": "POST",
    "path": "/template-batches"
  },
  "get_template_batches_id": {
    "method": "GET",
    "path": "/template-batches/{id}"
  },
  "get_template_batches_id_evolutions": {
    "method": "GET",
    "path": "/template-batches/{id}/evolutions"
  },
  "post_template_batches_id_evolve": {
    "method": "POST",
    "path": "/template-batches/{id}/evolve"
  },
  "post_template_batches_id_retry": {
    "method": "POST",
    "path": "/template-batches/{id}/retry"
  },
  "post_users": {
    "method": "POST",
    "path": "/users"
  },
  "get_work_orders": {
    "method": "GET",
    "path": "/work-orders"
  },
  "post_work_orders": {
    "method": "POST",
    "path": "/work-orders"
  },
  "get_work_orders_id": {
    "method": "GET",
    "path": "/work-orders/{id}"
  },
  "post_work_orders_id_actions": {
    "method": "POST",
    "path": "/work-orders/{id}/actions"
  },
  "post_work_orders_id_handovers": {
    "method": "POST",
    "path": "/work-orders/{id}/handovers"
  },
  "get_workload_identities": {
    "method": "GET",
    "path": "/workload-identities"
  },
  "post_workload_identities": {
    "method": "POST",
    "path": "/workload-identities"
  },
  "post_workload_identities_id_rotate": {
    "method": "POST",
    "path": "/workload-identities/{id}/rotate"
  }
} as const;
