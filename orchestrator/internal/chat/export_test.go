package chat

import (
	"context"
	"time"
)

// SetBeforeMake sets the hook run between keeping a routing decision and
// acting on it.
func (s *Service) SetBeforeMake(fn func(ctx context.Context) error) { s.beforeMake = fn }

// SetDeliveryTimeout bounds one attempt at a delivery, in place of DeliveryTimeout.
func (s *Service) SetDeliveryTimeout(d time.Duration) { s.deliveryTimeout = d }
