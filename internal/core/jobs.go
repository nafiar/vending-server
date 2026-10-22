package core

import (
	"context"
	"time"
)

// ExpirePending marks every PENDING transaction older than the payment window
// as EXPIRED (BR-06). The server runs it every minute, so an abandoned payment
// screen never holds its item for long. It returns how many it expired.
func (s *Service) ExpirePending(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE transactions SET status = 'EXPIRED', finished_at = DATE_ADD(created_at, INTERVAL ? SECOND)
		WHERE status = 'PENDING' AND created_at < ?`,
		int(s.paymentWindow/time.Second), s.now().Add(-s.paymentWindow))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CleanupSessions deletes sessions that passed their idle timeout.
func (s *Service) CleanupSessions(ctx context.Context) (int64, error) {
	now := s.now()
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM sessions
		WHERE (kind = 'ADMIN' AND last_seen_at < ?) OR (kind = 'OPERATOR' AND last_seen_at < ?)`,
		now.Add(-s.adminIdle), now.Add(-s.operatorIdle))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// RunJobs runs the periodic jobs until ctx ends.
func (s *Service) RunJobs(ctx context.Context) {
	expire := time.NewTicker(time.Minute)
	cleanup := time.NewTicker(10 * time.Minute)
	defer expire.Stop()
	defer cleanup.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-expire.C:
			if n, err := s.ExpirePending(ctx); err != nil {
				s.log.Error("expire pending transactions", "err", err)
			} else if n > 0 {
				s.log.Info("expired abandoned payments", "count", n)
			}
		case <-cleanup.C:
			if _, err := s.CleanupSessions(ctx); err != nil {
				s.log.Error("clean up sessions", "err", err)
			}
		}
	}
}
