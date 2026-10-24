package chat

import "context"

// SetBeforeMake sets the hook run between keeping a routing decision and
// acting on it.
func (s *Service) SetBeforeMake(fn func(ctx context.Context) error) { s.beforeMake = fn }
