package lux_test

import (
	"context"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// GET /v1/pools as today's lux answers it (docs/openapi.yaml Pool): no
// host sizes, no default flag.
const oldPools = `{"pools":[
 {"name":"default","provider":"ec2","minHosts":0,"maxHosts":4,"scaleDownAfter":"10m","template":{},"tenant":"acme"},
 {"name":"shared","provider":"static","platform":true,"shared":true}]}`

// The same after lux reports each pool's host size and where it has it from.
const newPools = `{"pools":[
 {"name":"default","provider":"ec2","isDefault":true,"instanceType":"c7a.4xlarge","hostsRunning":3,
  "hostSize":{"cpus":16,"memory":34359738368,"disk":193273528320},"hostSizeFrom":"running","tenant":"acme"},
 {"name":"big","provider":"ec2","instanceType":"c7a.8xlarge","hostsRunning":0,
  "hostSize":{"cpus":32,"memory":68719476736,"disk":408021893120},"hostSizeFrom":"history"},
 {"name":"new","provider":"ec2","hostSize":null}]}`

func TestPoolsReadsLuxWithOrWithoutHostSizes(t *testing.T) {
	url, path := costServer(t, 200, oldPools)
	pools, err := lux.New(url, "k").Pools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if *path != "GET /v1/pools" {
		t.Errorf("asked %s", *path)
	}
	if len(pools) != 2 || pools[0].Name != "default" || pools[0].HostSize != nil || pools[0].IsDefault || pools[0].HostsRunning != nil {
		t.Errorf("old pools = %+v", pools)
	}
	if !pools[1].Platform || pools[1].Provider != "static" {
		t.Errorf("shared = %+v", pools[1])
	}

	url, _ = costServer(t, 200, newPools)
	pools, err = lux.New(url, "k").Pools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	d := pools[0]
	if !d.IsDefault || d.InstanceType != "c7a.4xlarge" || d.HostSizeFrom != "running" || d.HostsRunning == nil || *d.HostsRunning != 3 ||
		d.HostSize == nil || *d.HostSize != (lux.HostSize{CPUs: 16, Memory: 32 << 30, Disk: 180 << 30}) {
		t.Errorf("default = %+v", d)
	}
	if pools[1].HostSizeFrom != "history" || pools[1].HostSize.Memory != 64<<30 {
		t.Errorf("big = %+v", pools[1])
	}
	if pools[2].HostSize != nil {
		t.Errorf("a pool with a null host size = %+v", pools[2])
	}
}

func TestPoolsPassesLuxsRefusalOn(t *testing.T) {
	url, _ := costServer(t, 403, `{"error":{"code":"forbidden","message":"missing scope read"}}`)
	if _, err := lux.New(url, "k").Pools(context.Background()); err == nil {
		t.Fatal("no error")
	} else if e, ok := lux.AsError(err); !ok || e.Status != 403 {
		t.Errorf("err = %v", err)
	}
}
