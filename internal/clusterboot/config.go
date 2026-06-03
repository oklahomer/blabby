// Package clusterboot holds blabby's proto.actor cluster bootstrap: parsing and
// validating the cluster flags, constructing the cluster, and logging
// membership changes. Keeping it in one place separates the cluster wiring from
// the server's HTTP/auth setup and keeps the entry point thin.
package clusterboot

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"strconv"
	"strings"
)

const (
	// defaultClusterHost is the remote transport's bind host when --cluster-host
	// is not given: loopback, matching the single-node zero-config default.
	defaultClusterHost = "127.0.0.1"

	// defaultDiscoveryPort is the automanaged provider's gossip port when
	// --discovery-port is not given. It matches the provider's own default so
	// the single-node path is unchanged.
	defaultDiscoveryPort = 6330
)

// Config is the parsed, validated proto.actor cluster bootstrap configuration.
// The cluster settings are a cohesive concern, built once via BindFlags (the
// flag boundary) or newClusterConfig and passed by value.
//
// The zero value (no seeds, empty advertised host) selects single-node mode and
// reproduces the zero-config default: the remote transport binds an ephemeral
// loopback port and the automanaged provider runs against its own defaults.
type Config struct {
	bindHost       string
	bindPort       int
	advertisedHost string
	discoveryPort  int
	seeds          []string
}

// MultiNode reports whether the operator opted into a real multi-node cluster.
// A non-empty discovery seed list is the signal: single-node mode never needs
// seeds, so any seed means peers are expected.
func (cc Config) MultiNode() bool {
	return len(cc.seeds) > 0
}

// Warnings returns advisory (non-fatal) notes about a multi-node configuration
// that is legal but likely a mistake, for the caller to log at startup. A
// loopback/unspecified advertised host works for a same-host multi-process demo
// but is unreachable from other machines; an advertised port that disagrees
// with the bind port is usually a typo, though legal behind a port-mapping
// proxy. Single-node configs never warn.
func (cc Config) Warnings() []string {
	if !cc.MultiNode() {
		return nil
	}
	host, port, err := net.SplitHostPort(cc.advertisedHost)
	if err != nil {
		// A malformed advertised host is rejected in newClusterConfig; if one
		// reaches here there is nothing meaningful to advise on.
		return nil
	}

	var warns []string
	if isLoopbackOrUnspecified(host) {
		warns = append(warns, fmt.Sprintf("advertised host %q is loopback/unspecified: reachable only on this machine, fine for a same-host demo but not across hosts", host))
	}
	if port != strconv.Itoa(cc.bindPort) {
		warns = append(warns, fmt.Sprintf("advertised port %s differs from --cluster-port %d: usually a misconfiguration unless a port-mapping proxy sits in front", port, cc.bindPort))
	}
	return warns
}

// Defaults are the default values BindFlags registers for the cluster flags.
// The binaries differ: a backend defaults to a single-node member (no seeds,
// ephemeral remote port), while a gateway defaults to a local demo against a
// loopback backend. Each binary supplies its own Defaults so the shared flag
// registration stays in one place without baking either role's policy into the
// package.
type Defaults struct {
	ClusterHost    string
	ClusterPort    int
	AdvertisedHost string
	DiscoveryPort  int
	Seeds          string
}

// MemberDefaults are a backend's zero-config defaults: a single-node member on a
// loopback ephemeral remote port with no discovery seeds.
func MemberDefaults() Defaults {
	return Defaults{
		ClusterHost:   defaultClusterHost,
		DiscoveryPort: defaultDiscoveryPort,
	}
}

// BindFlags registers the cluster flags on fs with d as their defaults, and
// returns a closure that builds and validates a Config from the parsed values.
// Call the closure after fs.Parse. Splitting registration from building lets a
// caller add its own flags to the same FlagSet (parse, don't validate at one
// boundary); the Defaults parameter lets each binary choose role-appropriate
// defaults without the package hardcoding either role.
func BindFlags(fs *flag.FlagSet, d Defaults) func() (Config, error) {
	bindHost := fs.String("cluster-host", d.ClusterHost, "cluster remote transport bind host")
	bindPort := fs.Int("cluster-port", d.ClusterPort, "cluster remote transport bind port; 0 binds an ephemeral port (single-node only)")
	advertisedHost := fs.String("advertised-host", d.AdvertisedHost, "host:port that peers use to reach this node; required in multi-node mode")
	discoveryPort := fs.Int("discovery-port", d.DiscoveryPort, "automanaged discovery (gossip) port")
	seeds := fs.String("seeds", d.Seeds, "comma-separated host:discoveryPort discovery seeds; any value selects multi-node mode")
	return func() (Config, error) {
		return newClusterConfig(*bindHost, *bindPort, *advertisedHost, *discoveryPort, *seeds)
	}
}

// newClusterConfig validates the raw cluster flag values into a Config (parse,
// don't validate). The seed list selects the mode. Single-node (no seeds) needs
// no further validation and is identical to today's default. Multi-node (one or
// more seeds) requires an explicit, peer-reachable address: peers reach this
// node at its advertised host:port, so an auto-derived or ephemeral address
// would silently break cross-node delivery (ADR-011).
func newClusterConfig(bindHost string, bindPort int, advertisedHost string, discoveryPort int, rawSeeds string) (Config, error) {
	cc := Config{
		bindHost:       strings.TrimSpace(bindHost),
		bindPort:       bindPort,
		advertisedHost: strings.TrimSpace(advertisedHost),
		discoveryPort:  discoveryPort,
		seeds:          parseSeeds(rawSeeds),
	}

	if !cc.MultiNode() {
		return cc, nil
	}

	if cc.advertisedHost == "" {
		return Config{}, errors.New("--advertised-host must be set in multi-node mode (one or more --seeds); it must not be auto-derived from the listener")
	}
	if cc.bindPort == 0 {
		return Config{}, errors.New("--cluster-port must be a fixed non-zero port in multi-node mode; an ephemeral port cannot be advertised to peers")
	}
	if _, _, err := net.SplitHostPort(cc.advertisedHost); err != nil {
		return Config{}, fmt.Errorf("--advertised-host %q is not a valid host:port: %w", cc.advertisedHost, err)
	}
	return cc, nil
}

// parseSeeds splits a comma-separated --seeds value into a clean list of
// host:discoveryPort endpoints, trimming surrounding whitespace and dropping
// empty entries. A blank or whitespace-only value yields nil, which selects
// single-node mode.
func parseSeeds(raw string) []string {
	parts := strings.Split(raw, ",")
	seeds := make([]string, 0, len(parts))
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			seeds = append(seeds, s)
		}
	}
	if len(seeds) == 0 {
		return nil
	}
	return seeds
}

// isLoopbackOrUnspecified reports whether host is a loopback or unspecified
// address — 127.0.0.1/::1, 0.0.0.0/::, or the hostname "localhost". Such a host
// is reachable in-process and across processes on one machine, but not from a
// peer on another host.
func isLoopbackOrUnspecified(host string) bool {
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || ip.IsUnspecified()
	}
	return false
}
