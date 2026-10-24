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

// RoutingSessions are the ids of the sessions the routing context offers
// person in a DM, newest first.
func (s *Service) RoutingSessions(ctx context.Context, org, person string) ([]string, error) {
	w, err := s.world(ctx, org, person, Mention{ConversationID: "D1"}, KindDM, nil)
	var ids []string
	for _, r := range w.Sessions {
		ids = append(ids, r.ID)
	}
	return ids, err
}
