package clusterboot

import (
	"flag"
	"io"
	"slices"
	"strings"
	"testing"
)

// parseClusterFlags drives BindFlags exactly as a binary would: register on a
// fresh FlagSet, parse args, then build-and-validate.
func parseClusterFlags(args []string) (Config, error) {
	fs := flag.NewFlagSet("clusterboot-test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	build := BindFlags(fs, MemberDefaults())
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	return build()
}

// TestBindFlagsHonorsDefaults verifies BindFlags registers the provided
// Defaults, so a binary that defaults to a multi-node client (e.g. the gateway)
// parses cleanly with no flags.
func TestBindFlagsHonorsDefaults(t *testing.T) {
	fs := flag.NewFlagSet("clusterboot-test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	build := BindFlags(fs, Defaults{
		ClusterHost:    "127.0.0.1",
		ClusterPort:    8091,
		AdvertisedHost: "127.0.0.1:8091",
		DiscoveryPort:  6331,
		Seeds:          "127.0.0.1:6330",
	})
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("parse with no args: %v", err)
	}
	cc, err := build()
	if err != nil {
		t.Fatalf("build with multi-node defaults: %v", err)
	}
	if !cc.MultiNode() {
		t.Errorf("MultiNode() = false, want true (seeds defaulted)")
	}
	if !slices.Equal(cc.seeds, []string{"127.0.0.1:6330"}) {
		t.Errorf("seeds = %v, want [127.0.0.1:6330]", cc.seeds)
	}
	if cc.advertisedHost != "127.0.0.1:8091" {
		t.Errorf("advertisedHost = %q, want 127.0.0.1:8091", cc.advertisedHost)
	}
	if cc.bindPort != 8091 {
		t.Errorf("bindPort = %d, want 8091", cc.bindPort)
	}
}

func TestBindFlags(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantErr  bool
		errMatch string

		wantBindHost       string
		wantBindPort       int
		wantAdvertisedHost string
		wantDiscoveryPort  int
		wantSeeds          []string
		wantMultiNode      bool
	}{
		{
			name:              "defaults (single-node)",
			args:              nil,
			wantBindHost:      defaultClusterHost,
			wantDiscoveryPort: defaultDiscoveryPort,
		},
		{
			name: "multi-node valid",
			args: []string{
				"--seeds", "127.0.0.1:6330,127.0.0.1:6331",
				"--advertised-host", "127.0.0.1:8091",
				"--cluster-port", "8091",
			},
			wantBindHost:       defaultClusterHost,
			wantBindPort:       8091,
			wantAdvertisedHost: "127.0.0.1:8091",
			wantDiscoveryPort:  defaultDiscoveryPort,
			wantSeeds:          []string{"127.0.0.1:6330", "127.0.0.1:6331"},
			wantMultiNode:      true,
		},
		{
			name: "multi-node with custom cluster-host and discovery-port",
			args: []string{
				"--seeds", "10.0.0.3:7000",
				"--advertised-host", "10.0.0.2:9100",
				"--cluster-host", "0.0.0.0",
				"--cluster-port", "9100",
				"--discovery-port", "7000",
			},
			wantBindHost:       "0.0.0.0",
			wantBindPort:       9100,
			wantAdvertisedHost: "10.0.0.2:9100",
			wantDiscoveryPort:  7000,
			wantSeeds:          []string{"10.0.0.3:7000"},
			wantMultiNode:      true,
		},
		{
			name: "seeds parsed with whitespace and empties dropped",
			args: []string{
				"--seeds", " 127.0.0.1:6330 , , 127.0.0.1:6331 ,,",
				"--advertised-host", "127.0.0.1:8091",
				"--cluster-port", "8091",
			},
			wantBindHost:       defaultClusterHost,
			wantBindPort:       8091,
			wantAdvertisedHost: "127.0.0.1:8091",
			wantDiscoveryPort:  defaultDiscoveryPort,
			wantSeeds:          []string{"127.0.0.1:6330", "127.0.0.1:6331"},
			wantMultiNode:      true,
		},
		{
			name:     "multi-node rejected: empty advertised host",
			args:     []string{"--seeds", "127.0.0.1:6330", "--cluster-port", "8091"},
			wantErr:  true,
			errMatch: "advertised-host",
		},
		{
			name:     "multi-node rejected: ephemeral cluster-port",
			args:     []string{"--seeds", "127.0.0.1:6330", "--advertised-host", "127.0.0.1:8091"},
			wantErr:  true,
			errMatch: "cluster-port",
		},
		{
			name:     "multi-node rejected: malformed advertised host",
			args:     []string{"--seeds", "127.0.0.1:6330", "--advertised-host", "not-a-hostport", "--cluster-port", "8091"},
			wantErr:  true,
			errMatch: "host:port",
		},
		{
			name:     "multi-node rejected: empty advertised host part",
			args:     []string{"--seeds", "127.0.0.1:6330", "--advertised-host", ":8091", "--cluster-port", "8091"},
			wantErr:  true,
			errMatch: "host",
		},
		{
			name:     "multi-node rejected: non-numeric advertised port",
			args:     []string{"--seeds", "127.0.0.1:6330", "--advertised-host", "127.0.0.1:abc", "--cluster-port", "8091"},
			wantErr:  true,
			errMatch: "port",
		},
		{
			name:     "multi-node rejected: out-of-range advertised port",
			args:     []string{"--seeds", "127.0.0.1:6330", "--advertised-host", "127.0.0.1:99999", "--cluster-port", "8091"},
			wantErr:  true,
			errMatch: "range",
		},
		{
			name:     "multi-node rejected: out-of-range cluster-port",
			args:     []string{"--seeds", "127.0.0.1:6330", "--advertised-host", "127.0.0.1:8091", "--cluster-port", "70000"},
			wantErr:  true,
			errMatch: "cluster-port",
		},
		{
			name:     "multi-node rejected: zero discovery-port",
			args:     []string{"--seeds", "127.0.0.1:6330", "--advertised-host", "127.0.0.1:8091", "--cluster-port", "8091", "--discovery-port", "0"},
			wantErr:  true,
			errMatch: "discovery-port",
		},
		{
			name:     "multi-node rejected: malformed seed (no port)",
			args:     []string{"--seeds", "127.0.0.1", "--advertised-host", "127.0.0.1:8091", "--cluster-port", "8091"},
			wantErr:  true,
			errMatch: "seeds",
		},
		{
			name:     "unknown flag rejected",
			args:     []string{"--nope"},
			wantErr:  true,
			errMatch: "nope",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseClusterFlags(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got config %+v", got)
				}
				if tc.errMatch != "" && !strings.Contains(err.Error(), tc.errMatch) {
					t.Fatalf("error %q does not contain %q", err, tc.errMatch)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.bindHost != tc.wantBindHost {
				t.Errorf("bindHost = %q, want %q", got.bindHost, tc.wantBindHost)
			}
			if got.bindPort != tc.wantBindPort {
				t.Errorf("bindPort = %d, want %d", got.bindPort, tc.wantBindPort)
			}
			if got.advertisedHost != tc.wantAdvertisedHost {
				t.Errorf("advertisedHost = %q, want %q", got.advertisedHost, tc.wantAdvertisedHost)
			}
			if got.discoveryPort != tc.wantDiscoveryPort {
				t.Errorf("discoveryPort = %d, want %d", got.discoveryPort, tc.wantDiscoveryPort)
			}
			if !slices.Equal(got.seeds, tc.wantSeeds) {
				t.Errorf("seeds = %v, want %v", got.seeds, tc.wantSeeds)
			}
			if got.MultiNode() != tc.wantMultiNode {
				t.Errorf("MultiNode() = %v, want %v", got.MultiNode(), tc.wantMultiNode)
			}
		})
	}
}

func TestConfigWarnings(t *testing.T) {
	tests := []struct {
		name     string
		cc       Config
		wantNone bool
		wantSubs []string // ordered substrings; len == expected warning count
	}{
		{
			name:     "single-node never warns",
			cc:       Config{bindHost: defaultClusterHost, discoveryPort: defaultDiscoveryPort},
			wantNone: true,
		},
		{
			name:     "loopback advertised host warns",
			cc:       Config{bindPort: 8091, advertisedHost: "127.0.0.1:8091", seeds: []string{"127.0.0.1:6330"}},
			wantSubs: []string{"loopback"},
		},
		{
			name:     "localhost advertised host warns",
			cc:       Config{bindPort: 8091, advertisedHost: "localhost:8091", seeds: []string{"127.0.0.1:6330"}},
			wantSubs: []string{"loopback"},
		},
		{
			name:     "routable host with matching port does not warn",
			cc:       Config{bindPort: 9100, advertisedHost: "10.0.0.2:9100", seeds: []string{"10.0.0.3:7000"}},
			wantNone: true,
		},
		{
			name:     "routable hostname with matching port does not warn",
			cc:       Config{bindPort: 9100, advertisedHost: "node1.internal:9100", seeds: []string{"10.0.0.3:7000"}},
			wantNone: true,
		},
		{
			name:     "advertised port mismatch warns",
			cc:       Config{bindPort: 9100, advertisedHost: "10.0.0.2:9999", seeds: []string{"10.0.0.3:7000"}},
			wantSubs: []string{"differs from --cluster-port"},
		},
		{
			name:     "loopback and port mismatch warns twice",
			cc:       Config{bindPort: 9100, advertisedHost: "127.0.0.1:9999", seeds: []string{"10.0.0.3:7000"}},
			wantSubs: []string{"loopback", "differs from --cluster-port"},
		},
		{
			name:     "malformed advertised host yields no advisory warnings",
			cc:       Config{bindPort: 9100, advertisedHost: "garbage-no-port", seeds: []string{"10.0.0.3:7000"}},
			wantNone: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.cc.Warnings()
			if tc.wantNone {
				if len(got) != 0 {
					t.Fatalf("Warnings() = %v, want none", got)
				}
				return
			}
			if len(got) != len(tc.wantSubs) {
				t.Fatalf("Warnings() = %v (%d), want %d warning(s)", got, len(got), len(tc.wantSubs))
			}
			for i, sub := range tc.wantSubs {
				if !strings.Contains(got[i], sub) {
					t.Errorf("warning[%d] = %q, want substring %q", i, got[i], sub)
				}
			}
		})
	}
}
