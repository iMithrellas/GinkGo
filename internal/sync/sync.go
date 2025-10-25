package sync

import "context"

// Service coordinates replication to remotes.
type Service struct{}

func New() *Service { return &Service{} }

func (s *Service) SyncNow(ctx context.Context) error {
	// Wireframe: no-op.
	return nil
}
