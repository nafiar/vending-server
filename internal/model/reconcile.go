package model

// SlotReconciliation checks one slot's bookkeeping: the count the last operator
// visit left, minus what was sold since, should equal the count held now
// (NFR-05).
//
// Slots that were never restocked are listed with NeverRestocked set and are
// expected to be at 0. LastVisitID and LastVisitAt are then meaningless, and
// CountAtVisit and SoldSince are zero.
type SlotReconciliation struct {
	MachineCode    string
	SlotNumber     int
	LastVisitID    int64
	LastVisitAt    int64
	CountAtVisit   int
	SoldSince      int
	Expected       int
	Actual         int
	Matches        bool
	NeverRestocked bool
}

// Reconciliation is the whole check: every slot, and how many of them do not
// add up. The count is carried with the rows because it is the answer -- a
// caller that had to add it up itself could get a different number from the
// screen next to it.
type Reconciliation struct {
	Slots      []SlotReconciliation
	Mismatches int
}

// Tallied returns the check with Mismatches counted from the rows.
func Tallied(slots []SlotReconciliation) Reconciliation {
	bad := 0
	for _, s := range slots {
		if !s.Matches {
			bad++
		}
	}
	return Reconciliation{Slots: slots, Mismatches: bad}
}
