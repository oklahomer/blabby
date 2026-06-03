package main

import (
	"strings"
	"testing"
)

// TestParseConfig covers the HTTP/auth flags the gateway owns and confirms the
// cluster flags registered by clusterboot.BindFlags flow through parseConfig —
// both mode selection and surfaced validation errors. The exhaustive
// cluster-flag cases live in the clusterboot package.
func TestParseConfig(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantErr  bool
		errMatch string

		wantListen    string
		wantSecret    string
		wantUsingDev  bool
		wantMultiNode bool
	}{
		{
			name:         "defaults",
			args:         nil,
			wantListen:   defaultListenAddr,
			wantSecret:   devJWTSecret,
			wantUsingDev: true,
		},
		{
			name:         "custom listen",
			args:         []string{"--listen", "127.0.0.1:9000"},
			wantListen:   "127.0.0.1:9000",
			wantSecret:   devJWTSecret,
			wantUsingDev: true,
		},
		{
			name:         "explicit secret disables dev default",
			args:         []string{"--jwt-secret", "s3cret"},
			wantListen:   defaultListenAddr,
			wantSecret:   "s3cret",
			wantUsingDev: false,
		},
		{
			name:         "blank secret falls back to dev default",
			args:         []string{"--jwt-secret", "   "},
			wantListen:   defaultListenAddr,
			wantSecret:   devJWTSecret,
			wantUsingDev: true,
		},
		{
			name:     "empty listen rejected",
			args:     []string{"--listen", "   "},
			wantErr:  true,
			errMatch: "must not be empty",
		},
		{
			name:     "listen without port rejected",
			args:     []string{"--listen", "localhost"},
			wantErr:  true,
			errMatch: "host:port",
		},
		{
			name:     "unknown flag rejected",
			args:     []string{"--nope"},
			wantErr:  true,
			errMatch: "nope",
		},
		{
			name: "cluster flags flow through: multi-node selected",
			args: []string{
				"--seeds", "127.0.0.1:6330",
				"--advertised-host", "127.0.0.1:8091",
				"--cluster-port", "8091",
			},
			wantListen:    defaultListenAddr,
			wantSecret:    devJWTSecret,
			wantUsingDev:  true,
			wantMultiNode: true,
		},
		{
			name:     "cluster validation error surfaces through parseConfig",
			args:     []string{"--seeds", "127.0.0.1:6330", "--cluster-port", "8091"},
			wantErr:  true,
			errMatch: "advertised-host",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotCfg, gotCluster, err := parseConfig(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got config %+v / cluster %+v", gotCfg, gotCluster)
				}
				if tc.errMatch != "" && !strings.Contains(err.Error(), tc.errMatch) {
					t.Fatalf("error %q does not contain %q", err, tc.errMatch)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotCfg.listenAddr != tc.wantListen {
				t.Errorf("listenAddr = %q, want %q", gotCfg.listenAddr, tc.wantListen)
			}
			if gotCfg.jwtSecret != tc.wantSecret {
				t.Errorf("jwtSecret = %q, want %q", gotCfg.jwtSecret, tc.wantSecret)
			}
			if gotCfg.usingDevSecret != tc.wantUsingDev {
				t.Errorf("usingDevSecret = %v, want %v", gotCfg.usingDevSecret, tc.wantUsingDev)
			}
			if gotCluster.MultiNode() != tc.wantMultiNode {
				t.Errorf("cluster.MultiNode() = %v, want %v", gotCluster.MultiNode(), tc.wantMultiNode)
			}
		})
	}
}
