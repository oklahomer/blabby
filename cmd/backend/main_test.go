package main

import (
	"strings"
	"testing"
)

// TestParseConfig confirms the backend exposes only the cluster flags (no
// HTTP/auth flags) and that clusterboot's validation surfaces through
// parseConfig. The exhaustive cluster-flag cases live in the clusterboot
// package.
func TestParseConfig(t *testing.T) {
	tests := []struct {
		name          string
		args          []string
		wantErr       bool
		errMatch      string
		wantMultiNode bool
	}{
		{
			name:          "defaults single-node",
			args:          nil,
			wantMultiNode: false,
		},
		{
			name: "multi-node valid",
			args: []string{
				"--seeds", "127.0.0.1:6330",
				"--advertised-host", "127.0.0.1:8092",
				"--cluster-port", "8092",
			},
			wantMultiNode: true,
		},
		{
			name:     "cluster validation error surfaces",
			args:     []string{"--seeds", "127.0.0.1:6330", "--cluster-port", "8092"},
			wantErr:  true,
			errMatch: "advertised-host",
		},
		{
			name:     "no HTTP flags: --listen rejected",
			args:     []string{"--listen", ":9000"},
			wantErr:  true,
			errMatch: "listen",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseConfig(tc.args)
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
			if got.MultiNode() != tc.wantMultiNode {
				t.Errorf("MultiNode() = %v, want %v", got.MultiNode(), tc.wantMultiNode)
			}
		})
	}
}
