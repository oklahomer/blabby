package gateway

import (
	"context"
	"fmt"

	"github.com/asynkron/protoactor-go/cluster"

	maintenancepb "github.com/oklahomer/blabby/gen/maintenance"
	"github.com/oklahomer/blabby/internal/grain/maintenance"
)

// MaintenanceTrigger starts a maintenance job and reports whether it began.
// accepted is false when the job is already running (the singleton grain coalesced
// the trigger). The internal job endpoint depends on this interface so it can be
// unit-tested with a fake; ClusterMaintenanceTrigger is the production
// implementation.
type MaintenanceTrigger interface {
	TriggerPendingAccountGC(ctx context.Context) (accepted bool, err error)
}

// ClusterMaintenanceTrigger triggers the pending-account GC by requesting the
// singleton maintenance grain through the cluster client.
type ClusterMaintenanceTrigger struct {
	cluster *cluster.Cluster
}

// NewClusterMaintenanceTrigger builds a ClusterMaintenanceTrigger over the gateway's
// cluster client.
func NewClusterMaintenanceTrigger(c *cluster.Cluster) *ClusterMaintenanceTrigger {
	return &ClusterMaintenanceTrigger{cluster: c}
}

// TriggerPendingAccountGC requests one sweep from the maintenance grain. The grain
// returns immediately without awaiting the sweep, so this call does not block on the
// database. ctx is part of the contract but unused: the grain call is bounded by the
// cluster's own request timeout, not by ctx.
func (t *ClusterMaintenanceTrigger) TriggerPendingAccountGC(_ context.Context) (bool, error) {
	client := maintenancepb.GetMaintenanceGrainGrainClient(t.cluster, maintenance.PendingAccountGCIdentity)
	resp, err := client.SweepPendingAccounts(&maintenancepb.SweepPendingAccountsRequest{})
	if err != nil {
		return false, fmt.Errorf("gateway: trigger pending-account GC: %w", err)
	}
	return resp.GetAccepted(), nil
}
