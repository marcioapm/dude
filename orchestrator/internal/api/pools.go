package api

import (
	"context"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// Pool is a lux pool as the machine sizes page shows it (the domain's
// MachinePool): lux's fields, with what an older lux does not send as null.
type Pool struct {
	Name         string        `json:"name"`
	IsDefault    bool          `json:"isDefault"`
	Platform     bool          `json:"platform"`
	Provider     *string       `json:"provider"`
	InstanceType *string       `json:"instanceType"`
	HostSize     *lux.HostSize `json:"hostSize"`
	HostSizeFrom *string       `json:"hostSizeFrom"`
	HostsRunning *int          `json:"hostsRunning"`
}

type poolList struct {
	Pools   []Pool    `json:"pools"`
	ReadAt  time.Time `json:"readAt"`
	Problem *string   `json:"problem"`
}

func (s *Server) pools(ctx context.Context) poolList {
	out := poolList{Pools: []Pool{}, ReadAt: time.Now().UTC()}
	if s.Lux == nil {
		problem := "lux is not configured"
		out.Problem = &problem
		return out
	}
	list, err := s.Lux.Pools(ctx)
	if err != nil {
		problem := "lux could not be read: " + err.Error()
		out.Problem = &problem
		return out
	}
	for _, p := range list {
		out.Pools = append(out.Pools, Pool{Name: p.Name, IsDefault: p.IsDefault, Platform: p.Platform,
			Provider: nonEmpty(p.Provider), InstanceType: nonEmpty(p.InstanceType), HostSize: p.HostSize,
			HostSizeFrom: nonEmpty(p.HostSizeFrom), HostsRunning: p.HostsRunning})
	}
	return out
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
