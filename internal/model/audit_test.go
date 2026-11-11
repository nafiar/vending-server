package model_test

import (
	"testing"

	"vending/internal/model"
)

// A change-log row should read as the change it was, so only the fields that
// actually differ are kept: a row saying the name is still "A" tells a reader
// nothing and buries the one thing that moved.
func TestChangesKeepsOnlyWhatMoved(t *testing.T) {
	b, a, changed := model.Changes(
		map[string]any{"name": "A", "price": int64(25000), "image": "x"},
		map[string]any{"name": "A", "price": int64(30000), "image": "x"})
	if !changed {
		t.Fatal("the price changed, so something changed")
	}
	if len(a) != 1 || a["price"] != int64(30000) || b["price"] != int64(25000) {
		t.Fatalf("got %v -> %v, want only the price", b, a)
	}
}

// A save that changed nothing writes no row at all, so "nothing changed" has to
// be reported as such rather than as an empty change.
func TestChangesReportsNoChange(t *testing.T) {
	if _, _, changed := model.Changes(
		map[string]any{"name": "A"}, map[string]any{"name": "A"}); changed {
		t.Error("the same value is not a change")
	}
	// nil and nil in particular: an untouched nullable column compared with
	// itself must not look like a change, or every save would log one.
	if _, _, changed := model.Changes(
		map[string]any{"x": nil}, map[string]any{"x": nil}); changed {
		t.Error("nil to nil is not a change")
	}
	if _, _, changed := model.Changes(nil, nil); changed {
		t.Error("nothing to nothing is not a change")
	}
}

// A field that only the "after" has is a change: that is what a value being set
// for the first time looks like.
func TestChangesTreatsANewlySetFieldAsAChange(t *testing.T) {
	b, a, changed := model.Changes(map[string]any{}, map[string]any{"note": "hi"})
	if !changed || a["note"] != "hi" || b["note"] != nil {
		t.Errorf("got %v -> %v, want the note set from nothing", b, a)
	}
}
