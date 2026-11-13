package model

import (
	"reflect"
	"time"
)

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

// MaxAuditLabelRunes is how much of an entity's name a log row keeps. The row
// has to stay readable years later, when the product itself may be long gone.
const MaxAuditLabelRunes = 150

// AuditEntry is one change-log row, written by the repository inside the
// caller's unit of work.
//
// Before and After hold only the fields that actually changed, so a row reads
// as the change it was: {"price": 25000} became {"price": 30000}. Before is nil
// for a CREATE, because nothing was there.
type AuditEntry struct {
	ActorID    int64
	Action     string
	EntityType string
	EntityID   int64
	Label      string
	Before     map[string]any // nil for CREATE
	After      map[string]any
	At         time.Time
}

// Changes keeps only the keys whose value differs between before and after, and
// reports whether anything did. A save that changed nothing writes no log row:
// a change log full of "nothing happened" is a change log nobody reads.
func Changes(before, after map[string]any) (b, a map[string]any, changed bool) {
	b, a = map[string]any{}, map[string]any{}
	for k, av := range after {
		if bv := before[k]; !reflect.DeepEqual(bv, av) {
			b[k], a[k] = bv, av
		}
	}
	return b, a, len(a) > 0
}

// MaxAuditRows is how many change-log rows one read returns. The change log is
// read by eye, not by machine: past a couple of thousand rows the answer is a
// narrower span, not a longer page.
const MaxAuditRows = 2000

// AuditEntityTypes are the entity types the change log can be filtered by.
var AuditEntityTypes = map[string]bool{
	EntityProduct: true, EntityMachine: true, EntitySlot: true, EntityUser: true,
}

// AuditFilter is the span of the change log to read, half-open as everywhere
// else: At >= Start AND At < End. EntityType empty means every type, and Limit
// caps the rows returned.
type AuditFilter struct {
	Start, End time.Time
	EntityType string
	Limit      int
}

// AuditLog is one change-log row read back for the screen, with the actor's
// name already resolved so the list needs no second lookup. The actor is
// flattened into the row rather than nested, as everywhere else in this package.
//
// Before and After are the stored JSON exactly as it was written, carried
// through untouched: re-encoding a log row is how a change log starts to
// disagree with what happened. Before is nil for a CREATE.
type AuditLog struct {
	ID          int64
	At          time.Time
	ActorID     int64
	ActorName   string
	Action      string
	EntityType  string
	EntityID    int64
	EntityLabel string
	Before      []byte
	After       []byte
}
