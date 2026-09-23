//go:build linux || darwin

package store

const (
	actionPlanned   = "planned"
	actionIssued    = "issued"
	actionNotSent   = "not-sent"
	actionUnknown   = "unknown"
	actionConfirmed = "confirmed"
	actionVerified  = "verified"
	actionConflict  = "conflict"
)

const (
	faultInitRecorded           = "init_recorded"
	faultInitGenerationsCreated = "init_generations_created"
	faultInitStagingCreated     = "init_staging_created"
	faultInitCatalogSync        = "init_catalog_sync"
	faultInitCatalogConfirmed   = "init_catalog_confirmed"
	faultInitMarkerRemoved      = "init_marker_removed"
	faultInitFinalized          = "init_finalized"
	faultStageSynced            = "stage_synced"
	faultGenerationPublished    = "generation_published"
	faultCatalogSynced          = "catalog_synced"
	faultCatalogReplaced        = "catalog_replaced"
	faultOperationReconcileSync = "operation_reconcile_sync"
)
