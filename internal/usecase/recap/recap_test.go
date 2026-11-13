package recap_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.uber.org/mock/gomock"

	"vending/internal/common/clock"
	"vending/internal/common/paging"
	"vending/internal/model"
	reportmock "vending/internal/repository/report/mock"
	txmock "vending/internal/repository/transaction/mock"
	"vending/internal/usecase/recap"
)

// The division of labour is what these tests are about. MySQL sums; this
// package decides which local day a sale falls in, which bucket that day
// belongs to, and which buckets exist at all. Everything below checks the
// second half, because the first half is a query and the second half is where
// an off-by-one week or a missing empty day comes from.

var (
	jakarta = func() *time.Location {
		loc, err := time.LoadLocation("Asia/Jakarta")
		if err != nil {
			panic(err)
		}
		return loc
	}()
	// 22 Oct 2026, 10:00 WIB.
	t0 = time.Date(2026, 10, 22, 3, 0, 0, 0, time.UTC)
)

type fixture struct {
	reports *reportmock.MockRepository
	history *txmock.MockHistory
	uc      *recap.Service
}

func newFixture(t *testing.T, now time.Time) (*fixture, *gomock.Controller) {
	ctrl := gomock.NewController(t)
	f := &fixture{
		reports: reportmock.NewMockRepository(ctrl),
		history: txmock.NewMockHistory(ctrl),
	}
	f.uc = recap.New(f.reports, f.history, clock.New(func() time.Time { return now }), jakarta, nil)
	return f, ctrl
}

func code(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("want an error, got none")
	}
	e, ok := model.AsError(err)
	if !ok {
		t.Fatalf("err = %v, want a model.Error", err)
	}
	return e.Code
}

// expectRecapReads is everything a recap asks for besides the series.
func (f *fixture) expectRecapReads() {
	f.reports.EXPECT().RecapTotals(gomock.Any(), gomock.Any()).Return(model.RecapTotals{}, nil)
	f.reports.EXPECT().RevenueByMachine(gomock.Any(), gomock.Any()).Return(nil, nil)
	f.reports.EXPECT().RevenueByProduct(gomock.Any(), gomock.Any()).Return(nil, nil)
}

// ---------------------------------------------------------------- summary

// "The same time yesterday" is yesterday up to the time of day it is now. A
// whole yesterday against a part of today would show a collapse every morning.
func TestSummaryComparesTodaySoFarWithYesterdaySoFar(t *testing.T) {
	f, ctrl := newFixture(t, t0)
	defer ctrl.Finish()
	// t0 is 22 Oct 10:00 WIB, so: today starts 21 Oct 17:00 UTC, yesterday
	// starts a day before that, and yesterday's cut-off is ten hours in.
	todayStart := time.Date(2026, 10, 21, 17, 0, 0, 0, time.UTC)
	f.reports.EXPECT().Summary(gomock.Any(), gomock.Any(), int64(0)).
		DoAndReturn(func(_ context.Context, w model.SummaryWindows, _ int64) (model.Summary, error) {
			if !w.TodayStart.Equal(todayStart) {
				t.Errorf("todayStart = %v, want %v", w.TodayStart, todayStart)
			}
			if !w.YesterdayStart.Equal(todayStart.AddDate(0, 0, -1)) {
				t.Errorf("yesterdayStart = %v", w.YesterdayStart)
			}
			// Ten hours into yesterday, the same as ten hours into today.
			if want := todayStart.AddDate(0, 0, -1).Add(10 * time.Hour); !w.YesterdayUntil.Equal(want) {
				t.Errorf("yesterdayUntil = %v, want %v", w.YesterdayUntil, want)
			}
			// Last 7 days is today and the six before it, not eight days.
			if want := todayStart.AddDate(0, 0, -6); !w.Last7Start.Equal(want) {
				t.Errorf("last7Start = %v, want %v", w.Last7Start, want)
			}
			// The month starts at local midnight on the 1st: 30 Sep 17:00 UTC.
			if want := time.Date(2026, 9, 30, 17, 0, 0, 0, time.UTC); !w.MonthStart.Equal(want) {
				t.Errorf("monthStart = %v, want %v", w.MonthStart, want)
			}
			return model.Summary{Today: model.Totals{Revenue: 50000, Sales: 2}}, nil
		})

	got, err := f.uc.Summary(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if got.Today.Revenue != 50000 {
		t.Errorf("summary = %+v", got)
	}
}

// Just after local midnight, "today" is the new day and yesterday's window is
// almost empty — not the previous UTC day, which is what makes this worth a
// test.
func TestSummaryJustAfterLocalMidnight(t *testing.T) {
	// 23 Oct 00:30 WIB is still 22 Oct 17:30 UTC.
	now := time.Date(2026, 10, 22, 17, 30, 0, 0, time.UTC)
	f, ctrl := newFixture(t, now)
	defer ctrl.Finish()
	f.reports.EXPECT().Summary(gomock.Any(), gomock.Any(), int64(0)).
		DoAndReturn(func(_ context.Context, w model.SummaryWindows, _ int64) (model.Summary, error) {
			// Local midnight of 23 Oct WIB is 22 Oct 17:00 UTC.
			if want := time.Date(2026, 10, 22, 17, 0, 0, 0, time.UTC); !w.TodayStart.Equal(want) {
				t.Errorf("todayStart = %v, want %v", w.TodayStart, want)
			}
			// Half an hour into yesterday, matching half an hour into today.
			if want := w.YesterdayStart.Add(30 * time.Minute); !w.YesterdayUntil.Equal(want) {
				t.Errorf("yesterdayUntil = %v, want %v", w.YesterdayUntil, want)
			}
			return model.Summary{}, nil
		})

	if _, err := f.uc.Summary(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
}

func TestSummaryPassesTheMachineFilterThrough(t *testing.T) {
	f, ctrl := newFixture(t, t0)
	defer ctrl.Finish()
	f.reports.EXPECT().Summary(gomock.Any(), gomock.Any(), int64(9)).Return(model.Summary{}, nil)

	if _, err := f.uc.Summary(context.Background(), 9); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------- the chart

// A day with no sales has no row to return, so the chart is filled in here. A
// chart that skipped it would make a quiet week look like a short one.
func TestRecapFillsInTheDaysWithNoSales(t *testing.T) {
	f, ctrl := newFixture(t, t0)
	defer ctrl.Finish()
	f.expectRecapReads()
	f.reports.EXPECT().RevenueByLocalDay(gomock.Any(), gomock.Any(), 7*3600).
		Return(map[string]model.Totals{
			"2026-10-02": {Revenue: 30000, Sales: 2},
		}, nil)

	got, err := f.uc.Recap(context.Background(), recap.Query{From: "2026-10-01", To: "2026-10-05"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Series) != 5 {
		t.Fatalf("series = %d points, want one per day", len(got.Series))
	}
	for i, want := range []int64{0, 30000, 0, 0, 0} {
		if got.Series[i].Totals.Revenue != want {
			t.Errorf("series[%d] = %d, want %d", i, got.Series[i].Totals.Revenue, want)
		}
	}
	if got.Series[0].Period != "2026-10-01" || got.Series[4].Period != "2026-10-05" {
		t.Errorf("series runs %s..%s", got.Series[0].Period, got.Series[4].Period)
	}
	if got.From != "2026-10-01" || got.To != "2026-10-05" || got.GroupBy != "day" {
		t.Errorf("recap = %+v", got)
	}
}

// Days are folded into weeks here, not in the query, so that "a week starts on
// Monday" stays one idea in one place.
func TestRecapFoldsDaysIntoWeeksAndMonths(t *testing.T) {
	t.Run("week", func(t *testing.T) {
		f, ctrl := newFixture(t, t0)
		defer ctrl.Finish()
		f.expectRecapReads()
		// 1 Oct is a Thursday and 5 Oct is the Monday after, so these two days
		// belong to different weeks even though they are four days apart.
		f.reports.EXPECT().RevenueByLocalDay(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(map[string]model.Totals{
				"2026-10-01": {Revenue: 10000, Sales: 1},
				"2026-10-02": {Revenue: 20000, Sales: 2},
				"2026-10-05": {Revenue: 40000, Sales: 4},
			}, nil)

		got, err := f.uc.Recap(context.Background(),
			recap.Query{From: "2026-10-01", To: "2026-10-05", GroupBy: "week"})
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Series) != 2 {
			t.Fatalf("series = %+v, want two weeks", got.Series)
		}
		// The first bucket starts before the range: the week of Monday 28 Sep.
		if got.Series[0].Period != "2026-09-28" || got.Series[0].Totals.Revenue != 30000 {
			t.Errorf("week 1 = %+v, want 1 and 2 Oct together", got.Series[0])
		}
		if got.Series[1].Period != "2026-10-05" || got.Series[1].Totals.Revenue != 40000 {
			t.Errorf("week 2 = %+v", got.Series[1])
		}
		if got.Series[0].Start.Weekday() != time.Monday {
			t.Errorf("a week starts on %s", got.Series[0].Start.Weekday())
		}
	})
	t.Run("month", func(t *testing.T) {
		f, ctrl := newFixture(t, t0)
		defer ctrl.Finish()
		f.expectRecapReads()
		f.reports.EXPECT().RevenueByLocalDay(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(map[string]model.Totals{
				"2026-09-30": {Revenue: 10000, Sales: 1},
				"2026-10-01": {Revenue: 20000, Sales: 2},
				"2026-10-31": {Revenue: 30000, Sales: 3},
			}, nil)

		got, err := f.uc.Recap(context.Background(),
			recap.Query{From: "2026-09-30", To: "2026-10-31", GroupBy: "month"})
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Series) != 2 {
			t.Fatalf("series = %+v, want two months", got.Series)
		}
		if got.Series[0].Period != "2026-09" || got.Series[0].Totals.Revenue != 10000 {
			t.Errorf("september = %+v", got.Series[0])
		}
		if got.Series[1].Period != "2026-10" || got.Series[1].Totals.Revenue != 50000 {
			t.Errorf("october = %+v", got.Series[1])
		}
	})
}

// The zone's offset is passed down so the query groups by local day. For WIB it
// is seven hours, and a sale at 23:00 local belongs to that local day rather
// than to the next UTC one.
func TestRecapAsksForLocalDaysNotUTCDays(t *testing.T) {
	f, ctrl := newFixture(t, t0)
	defer ctrl.Finish()
	f.expectRecapReads()
	f.reports.EXPECT().RevenueByLocalDay(gomock.Any(), gomock.Any(), 7*3600).Return(nil, nil)

	if _, err := f.uc.Recap(context.Background(), recap.Query{From: "2026-10-01", To: "2026-10-01"}); err != nil {
		t.Fatal(err)
	}
}

// The span is passed to every read as the same half-open window, so the figures
// in one recap are all about the same period.
func TestRecapPassesOneSpanToEveryRead(t *testing.T) {
	f, ctrl := newFixture(t, t0)
	defer ctrl.Finish()
	// 1 Oct 00:00 WIB is 30 Sep 17:00 UTC; the end is exclusive, so 6 Oct.
	wantStart := time.Date(2026, 9, 30, 17, 0, 0, 0, time.UTC)
	wantEnd := time.Date(2026, 10, 5, 17, 0, 0, 0, time.UTC)
	check := func(f model.ReportFilter) {
		if !f.Start.Equal(wantStart) || !f.End.Equal(wantEnd) {
			t.Errorf("filter = %v..%v, want %v..%v", f.Start, f.End, wantStart, wantEnd)
		}
		if f.MachineID != 9 {
			t.Errorf("machineID = %d, want 9", f.MachineID)
		}
	}
	f.reports.EXPECT().RecapTotals(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, flt model.ReportFilter) (model.RecapTotals, error) {
			check(flt)
			return model.RecapTotals{}, nil
		})
	f.reports.EXPECT().RevenueByLocalDay(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, flt model.ReportFilter, _ int) (map[string]model.Totals, error) {
			check(flt)
			return nil, nil
		})
	f.reports.EXPECT().RevenueByMachine(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, flt model.ReportFilter) ([]model.MachineRevenue, error) {
			check(flt)
			return nil, nil
		})
	f.reports.EXPECT().RevenueByProduct(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, flt model.ReportFilter) ([]model.ProductRevenue, error) {
			check(flt)
			return nil, nil
		})

	_, err := f.uc.Recap(context.Background(),
		recap.Query{From: "2026-10-01", To: "2026-10-05", MachineID: 9})
	if err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------- breakdowns

// Top products is always by product, however the breakdown is grouped: "what
// sells" and "where did the money come from" are different questions.
func TestRecapTopProductsStayByProductEvenWhenBrokenDownBySlot(t *testing.T) {
	f, ctrl := newFixture(t, t0)
	defer ctrl.Finish()
	f.reports.EXPECT().RecapTotals(gomock.Any(), gomock.Any()).Return(model.RecapTotals{}, nil)
	f.reports.EXPECT().RevenueByLocalDay(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil)
	f.reports.EXPECT().RevenueByMachine(gomock.Any(), gomock.Any()).Return(nil, nil)
	f.reports.EXPECT().RevenueByProduct(gomock.Any(), gomock.Any()).
		Return([]model.ProductRevenue{{ProductID: 7, Name: "Gacha", Totals: model.Totals{Revenue: 50000, Sales: 2}}}, nil)
	name := "Gacha"
	f.reports.EXPECT().RevenueBySlot(gomock.Any(), gomock.Any()).
		Return([]model.SlotRevenue{{
			MachineID: 9, MachineCode: "VM-01", SlotNumber: 3, CurrentProduct: &name,
			Totals: model.Totals{Revenue: 50000, Sales: 2},
		}}, nil)

	got, err := f.uc.Recap(context.Background(), recap.Query{Breakdown: "slot"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.TopProducts) != 1 || got.TopProducts[0].Label != "Gacha" {
		t.Errorf("topProducts = %+v, want the product rows", got.TopProducts)
	}
	if len(got.Breakdown) != 1 {
		t.Fatalf("breakdown = %+v", got.Breakdown)
	}
	// The key names the machine and the slot, because slot numbers repeat
	// across machines and a row has to identify one coil.
	if got.Breakdown[0].Key != "9-3" {
		t.Errorf("key = %q, want 9-3", got.Breakdown[0].Key)
	}
	if got.Breakdown[0].Label != "VM-01 · Slot 3 (Gacha)" {
		t.Errorf("label = %q", got.Breakdown[0].Label)
	}
}

// A slot that holds nothing today still has its figures, and its label just
// does not name a product.
func TestRecapLabelsASlotThatHoldsNothing(t *testing.T) {
	f, ctrl := newFixture(t, t0)
	defer ctrl.Finish()
	f.reports.EXPECT().RecapTotals(gomock.Any(), gomock.Any()).Return(model.RecapTotals{}, nil)
	f.reports.EXPECT().RevenueByLocalDay(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil)
	f.reports.EXPECT().RevenueByMachine(gomock.Any(), gomock.Any()).Return(nil, nil)
	f.reports.EXPECT().RevenueByProduct(gomock.Any(), gomock.Any()).Return(nil, nil)
	f.reports.EXPECT().RevenueBySlot(gomock.Any(), gomock.Any()).
		Return([]model.SlotRevenue{{MachineID: 9, MachineCode: "VM-01", SlotNumber: 3,
			Totals: model.Totals{Revenue: 10000, Sales: 1}}}, nil)

	got, err := f.uc.Recap(context.Background(), recap.Query{Breakdown: "slot"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Breakdown[0].Label != "VM-01 · Slot 3" {
		t.Errorf("label = %q, want no product named", got.Breakdown[0].Label)
	}
}

// By default the breakdown is by product, and the slot query is not run at all.
func TestRecapDefaultsToTheProductBreakdown(t *testing.T) {
	f, ctrl := newFixture(t, t0)
	defer ctrl.Finish()
	f.expectRecapReads()
	f.reports.EXPECT().RevenueByLocalDay(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil)
	// No RevenueBySlot is expected.

	got, err := f.uc.Recap(context.Background(), recap.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if got.GroupBy != "day" {
		t.Errorf("groupBy = %q, want day", got.GroupBy)
	}
}

// Only the best few products are highlighted, however many there are.
func TestRecapKeepsOnlyTheTopFiveProducts(t *testing.T) {
	f, ctrl := newFixture(t, t0)
	defer ctrl.Finish()
	var many []model.ProductRevenue
	for i := int64(1); i <= 9; i++ {
		many = append(many, model.ProductRevenue{
			ProductID: i, Name: "P", Totals: model.Totals{Revenue: 1000 * (10 - i)},
		})
	}
	f.reports.EXPECT().RecapTotals(gomock.Any(), gomock.Any()).Return(model.RecapTotals{}, nil)
	f.reports.EXPECT().RevenueByLocalDay(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil)
	f.reports.EXPECT().RevenueByMachine(gomock.Any(), gomock.Any()).Return(nil, nil)
	f.reports.EXPECT().RevenueByProduct(gomock.Any(), gomock.Any()).Return(many, nil)

	got, err := f.uc.Recap(context.Background(), recap.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.TopProducts) != 5 {
		t.Errorf("topProducts = %d, want 5", len(got.TopProducts))
	}
	// The full set is still in the breakdown: the cap is for the highlight.
	if len(got.Breakdown) != 9 {
		t.Errorf("breakdown = %d, want all 9", len(got.Breakdown))
	}
}

// ---------------------------------------------------------------- refusals

func TestRecapRefusesParametersItCannotUse(t *testing.T) {
	cases := []struct {
		name  string
		query recap.Query
		field string
	}{
		{"an unknown grouping", recap.Query{GroupBy: "hour"}, "groupBy"},
		{"an unknown breakdown", recap.Query{Breakdown: "machine"}, "breakdown"},
		{"a backwards range", recap.Query{From: "2026-10-05", To: "2026-10-01"}, "from"},
		{"a range that is not a date", recap.Query{From: "kemarin"}, "from"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, ctrl := newFixture(t, t0)
			defer ctrl.Finish()
			// Nothing is read: the parameters are refused first.

			_, err := f.uc.Recap(context.Background(), tc.query)
			if got := code(t, err); got != model.CodeValidation {
				t.Fatalf("code = %s, want %s", got, model.CodeValidation)
			}
			e, _ := model.AsError(err)
			if e.Fields[tc.field] == "" {
				t.Errorf("fields = %v, want %s named", e.Fields, tc.field)
			}
		})
	}
}

// Both parameters are reported when both are wrong, so a screen hears about
// both rather than one per request.
func TestRecapReportsBothBadParametersAtOnce(t *testing.T) {
	f, ctrl := newFixture(t, t0)
	defer ctrl.Finish()

	_, err := f.uc.Recap(context.Background(), recap.Query{GroupBy: "hour", Breakdown: "machine"})
	e, _ := model.AsError(err)
	if e.Fields["groupBy"] == "" || e.Fields["breakdown"] == "" {
		t.Errorf("fields = %v, want both named", e.Fields)
	}
}

// A storage failure is reported as itself, so an outage does not look to the
// admin like a month with no sales.
func TestARepositoryFailureIsReported(t *testing.T) {
	f, ctrl := newFixture(t, t0)
	defer ctrl.Finish()
	boom := errors.New("the transactions table is unreachable")
	f.reports.EXPECT().RecapTotals(gomock.Any(), gomock.Any()).Return(model.RecapTotals{}, boom)

	if _, err := f.uc.Recap(context.Background(), recap.Query{}); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the repository's", err)
	}
}

// ---------------------------------------------------------------- history

// The history's own job is small and worth pinning down: it turns a page
// request into a window, refuses a filter it does not understand, and says
// which page it answered with. The rows themselves are the repository's.

func TestHistoryWindowAndEchoedPage(t *testing.T) {
	f, ctrl := newFixture(t, t0)
	defer ctrl.Finish()

	// Page 3 of 20 is rows 40 to 59, and the span defaults to the last week:
	// 16 Oct 00:00 WIB up to the end of 22 Oct, both as UTC instants.
	f.history.EXPECT().List(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, got model.TransactionFilter) (model.TransactionHistory, error) {
			if got.Limit != 20 || got.Offset != 40 {
				t.Errorf("window = limit %d offset %d, want 20 and 40", got.Limit, got.Offset)
			}
			wantStart := time.Date(2026, 10, 16, 0, 0, 0, 0, jakarta).UTC()
			wantEnd := time.Date(2026, 10, 23, 0, 0, 0, 0, jakarta).UTC()
			if !got.Start.Equal(wantStart) || !got.End.Equal(wantEnd) {
				t.Errorf("span = %s..%s, want %s..%s", got.Start, got.End, wantStart, wantEnd)
			}
			return model.TransactionHistory{Total: 4812}, nil
		})

	out, err := f.uc.History(context.Background(), recap.HistoryQuery{Page: 3, PageSize: 20})
	if err != nil {
		t.Fatal(err)
	}
	if out.Page != 3 || out.PageSize != 20 || out.Total != 4812 {
		t.Fatalf("got page %d size %d total %d, want 3, 20 and 4812", out.Page, out.PageSize, out.Total)
	}
}

// An export asks for every matching attempt, so the page number stops meaning
// anything: it reads from the first row, capped.
func TestHistoryExportIgnoresThePage(t *testing.T) {
	f, ctrl := newFixture(t, t0)
	defer ctrl.Finish()

	f.history.EXPECT().List(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, got model.TransactionFilter) (model.TransactionHistory, error) {
			if got.Offset != 0 || got.Limit != paging.MaxExportRows {
				t.Errorf("export window = limit %d offset %d, want %d and 0", got.Limit, got.Offset, paging.MaxExportRows)
			}
			return model.TransactionHistory{}, nil
		})

	if _, err := f.uc.History(context.Background(), recap.HistoryQuery{Page: 7, PageSize: 20, All: true}); err != nil {
		t.Fatal(err)
	}
}

// A filter nobody understands is refused, not dropped. Dropping it would show
// every attempt on a screen that says it is showing the failed ones, and the
// repository is never asked.
func TestHistoryRefusesUnknownFilters(t *testing.T) {
	for _, c := range []struct {
		name  string
		query recap.HistoryQuery
		field string
	}{
		{"status", recap.HistoryQuery{Status: "LOST"}, "status"},
		{"review flag", recap.HistoryQuery{Flag: "weird"}, "flag"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, ctrl := newFixture(t, t0)
			defer ctrl.Finish()
			// No EXPECT on the history: it must not be reached.
			_, err := f.uc.History(context.Background(), c.query)
			if got := code(t, err); got != model.CodeValidation {
				t.Fatalf("code = %s, want %s", got, model.CodeValidation)
			}
			e, _ := model.AsError(err)
			if _, ok := e.Fields[c.field]; !ok {
				t.Fatalf("fields = %v, want one on %q", e.Fields, c.field)
			}
		})
	}
}
