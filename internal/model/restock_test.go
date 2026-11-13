package model_test

import (
	"testing"

	"vending/internal/model"
)

// Tally is how the admin history reads a visit, and the product-change case is
// the one worth pinning: a change empties the coil and refills it, so what is
// in it afterwards was all put there at this visit (FR-16) -- even when the
// count went down and nothing looks like it was "added".
func TestTally(t *testing.T) {
	cases := []struct {
		name                string
		items               []model.RestockVisitItem
		added, slotsChanged int
	}{
		{
			name:  "a refill counts the difference",
			items: []model.RestockVisitItem{{QtyBefore: 2, QtyAfter: 10}},
			added: 8, slotsChanged: 1,
		},
		{
			name:  "a product change counts the whole new count",
			items: []model.RestockVisitItem{{QtyBefore: 9, QtyAfter: 4, ProductChanged: true}},
			added: 4, slotsChanged: 1,
		},
		{
			name:  "a slot nobody touched counts as nothing",
			items: []model.RestockVisitItem{{QtyBefore: 5, QtyAfter: 5}},
			added: 0, slotsChanged: 0,
		},
		{
			name:  "selling out before the visit is not a change the visit made",
			items: []model.RestockVisitItem{{QtyBefore: 3, QtyAfter: 0}},
			added: 0, slotsChanged: 1,
		},
		{
			name: "a whole machine adds up",
			items: []model.RestockVisitItem{
				{QtyBefore: 0, QtyAfter: 10},
				{QtyBefore: 4, QtyAfter: 4},
				{QtyBefore: 7, QtyAfter: 2, ProductChanged: true},
			},
			added: 12, slotsChanged: 2,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			added, changed := model.Tally(c.items)
			if added != c.added || changed != c.slotsChanged {
				t.Fatalf("Tally = %d added, %d changed; want %d and %d", added, changed, c.added, c.slotsChanged)
			}
		})
	}
}
