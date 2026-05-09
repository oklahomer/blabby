package connection

import (
	"fmt"

	"github.com/asynkron/protoactor-go/cluster"

	userpb "github.com/oklahomer/blabby/gen/user"
)

// clusterUserGrainCaller routes RegisterConnection through the protoactor
// cluster client. It exists so the actor can depend on the small
// UserGrainCaller seam (testable) while production wiring still goes
// through the generated grain client.
type clusterUserGrainCaller struct {
	cluster *cluster.Cluster
}

func newClusterUserGrainCaller(c *cluster.Cluster) *clusterUserGrainCaller {
	return &clusterUserGrainCaller{cluster: c}
}

// RegisterConnection forwards the request to the User grain identified by
// userID via the generated cluster client.
func (c *clusterUserGrainCaller) RegisterConnection(userID string, req *userpb.RegisterConnectionRequest) (*userpb.RegisterConnectionResponse, error) {
	resp, err := userpb.GetUserGrainGrainClient(c.cluster, userID).RegisterConnection(req)
	if err != nil {
		return nil, fmt.Errorf("user grain RegisterConnection: %w", err)
	}
	return resp, nil
}
