package model

// SlotReconciliation checks one slot's bookkeeping: the count the last operator
// visit left, minus what was sold since, should equal the count held now.
// Slots that were never restocked are listed with NeverRestocked set and are
// expected to be at 0.
type SlotReconciliation struct {
	MachineCode    string `json:"machineCode"`
	SlotNumber     int    `json:"slotNumber"`
	LastVisitID    int64  `json:"lastVisitId"`
	LastVisitAt    int64  `json:"lastVisitAt"`
	CountAtVisit   int    `json:"countAtVisit"`
	SoldSince      int    `json:"soldSince"`
	Expected       int    `json:"expected"`
	Actual         int    `json:"actual"`
	Matches        bool   `json:"matches"`
	NeverRestocked bool   `json:"neverRestocked"`
}
