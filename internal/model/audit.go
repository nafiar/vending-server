package model

// Audit actions and entity types, as in the audit_logs ENUMs. Every write an
// admin makes records one row in the same database transaction (FR-26).
const (
	ActionCreate     = "CREATE"
	ActionUpdate     = "UPDATE"
	ActionArchive    = "ARCHIVE"
	ActionRestore    = "RESTORE"
	ActionDeactivate = "DEACTIVATE"
	ActionActivate   = "ACTIVATE"

	EntityProduct = "PRODUCT"
	EntityMachine = "MACHINE"
	EntitySlot    = "SLOT"
	EntityUser    = "USER"
)

// AuditEntry is one change-log row, written by the repository inside the
// caller's unit of work.
type AuditEntry struct {
	ActorID    int64
	Action     string
	EntityType string
	EntityID   int64
	Label      string
	Before     map[string]any // nil for CREATE
	After      map[string]any
}
