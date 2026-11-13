package scheduler_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"go.uber.org/mock/gomock"

	"vending/internal/delivery/scheduler"
	mock "vending/internal/usecase/maintenance/mock"
)

// The scheduler decides nothing, so what is worth testing is the two things it
// does own: that start-up sweeps once before the first request, and that a
// failed sweep does not take the loop down with it.

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// Catchup is the start-up sweep. A server stopped overnight comes back holding
// items for payments nobody will make, and the first tick is a minute away.
func TestCatchupSweepsOnce(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	uc := mock.NewMockUseCase(ctrl)

	uc.EXPECT().ExpirePending(gomock.Any()).Return(int64(4), nil).Times(1)

	scheduler.New(uc, quiet()).Catchup(context.Background())
}

// A sweep that fails is logged and left for the next tick: nothing is waiting
// on it, and the same rows are still there to sweep in a minute.
func TestCatchupSurvivesAFailure(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	uc := mock.NewMockUseCase(ctrl)

	uc.EXPECT().ExpirePending(gomock.Any()).Return(int64(0), errors.New("database is away"))

	// The point is that this returns at all rather than panicking or blocking.
	scheduler.New(uc, quiet()).Catchup(context.Background())
}

// Run belongs to the process's lifetime, so it has to stop when the context
// does: a shutdown that waits on a ten-minute ticker is a shutdown that hangs.
func TestRunStopsWithTheContext(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	uc := mock.NewMockUseCase(ctrl)
	// No EXPECT: cancelled before either ticker (a minute, ten minutes) fires.

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		scheduler.New(uc, quiet()).Run(ctx)
	}()

	cancel()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return when its context was cancelled")
	}
}
