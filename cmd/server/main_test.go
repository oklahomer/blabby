package main

import (
	"strings"
	"testing"
)

func TestParseConfig(t *testing.T) {
	tests := []struct {
		name         string
		args         []string
		wantErr      bool
		errMatch     string
		wantListen   string
		wantSecret   string
		wantUsingDev bool
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
			if got.listenAddr != tc.wantListen {
				t.Errorf("listenAddr = %q, want %q", got.listenAddr, tc.wantListen)
			}
			if got.jwtSecret != tc.wantSecret {
				t.Errorf("jwtSecret = %q, want %q", got.jwtSecret, tc.wantSecret)
			}
			if got.usingDevSecret != tc.wantUsingDev {
				t.Errorf("usingDevSecret = %v, want %v", got.usingDevSecret, tc.wantUsingDev)
			}
		})
	}
}

func TestClusterKindsRegistersUserAndRoom(t *testing.T) {
	kinds := clusterKinds(nil)

	got := make(map[string]bool, len(kinds))
	for _, k := range kinds {
		if k == nil {
			t.Fatal("clusterKinds returned a nil kind")
		}
		got[k.Kind] = true
	}

	for _, want := range []string{"UserGrain", "RoomGrain"} {
		if !got[want] {
			t.Errorf("clusterKinds missing %q kind; got %v", want, got)
		}
	}
	if len(kinds) != 2 {
		t.Errorf("clusterKinds returned %d kinds, want 2", len(kinds))
	}
}
