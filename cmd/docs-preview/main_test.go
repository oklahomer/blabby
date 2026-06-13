package main

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		args    []string
		want    config
		wantErr string
	}{
		{name: "defaults", want: config{docsPort: 8081, asyncAPIPort: 8082}},
		{name: "overrides", args: []string{"--port", "9081", "--asyncapi-port", "9082"}, want: config{docsPort: 9081, asyncAPIPort: 9082}},
		{name: "zero landing port", args: []string{"--port", "0"}, wantErr: "--port must be between 1 and 65535"},
		{name: "oversized AsyncAPI port", args: []string{"--asyncapi-port", "65536"}, wantErr: "--asyncapi-port must be between 1 and 65535"},
		{name: "same ports", args: []string{"--port", "8081", "--asyncapi-port", "8081"}, wantErr: "must be different"},
		{name: "positional argument", args: []string{"unexpected"}, wantErr: "unexpected arguments"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseConfig(tt.args)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("parseConfig() error = %v, want substring %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseConfig() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("parseConfig() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestPreviewHandler(t *testing.T) {
	t.Parallel()

	const (
		openAPIHTML = "<html>openapi</html>"
		asyncAPIURL = "http://localhost:8082?previewServer=8082&studio-version=1.2.0"
	)
	handler := newPreviewHandler([]byte(openAPIHTML), asyncAPIURL)

	t.Run("landing page", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, "http://docs.test/", nil)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		resp := recorder.Result()
		defer closeBody(t, resp.Body)
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"blabby API contracts", `href="/openapi"`, `href="/asyncapi"`} {
			if !strings.Contains(string(body), want) {
				t.Errorf("landing page missing %q", want)
			}
		}
	})

	t.Run("OpenAPI HTML", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, "http://docs.test/openapi", nil)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		resp := recorder.Result()
		defer closeBody(t, resp.Body)
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != openAPIHTML {
			t.Fatalf("body = %q, want %q", body, openAPIHTML)
		}
		if got := resp.Header.Get("Content-Type"); got != "text/html; charset=utf-8" {
			t.Fatalf("Content-Type = %q", got)
		}
	})

	t.Run("AsyncAPI redirect", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, "http://docs.test/asyncapi", nil)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		resp := recorder.Result()
		defer closeBody(t, resp.Body)
		if resp.StatusCode != http.StatusTemporaryRedirect {
			t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusTemporaryRedirect)
		}
		if got := resp.Header.Get("Location"); got != asyncAPIURL {
			t.Fatalf("Location = %q, want %q", got, asyncAPIURL)
		}
	})

	t.Run("unknown route", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, "http://docs.test/unknown", nil)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		resp := recorder.Result()
		defer closeBody(t, resp.Body)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
		}
	})
}

func TestPreviewURLFinderHandlesFragmentedOutput(t *testing.T) {
	t.Parallel()

	ready := make(chan string, 1)
	finder := &previewURLFinder{publisher: &urlPublisher{ready: ready}}
	parts := []string{
		"noise\nOpen this URL in your ",
		"web browser: http://localhost:8082?previewServer=8082&studio-version=1.2.0\n",
	}
	for _, part := range parts {
		if _, err := finder.Write([]byte(part)); err != nil {
			t.Fatal(err)
		}
	}

	select {
	case got := <-ready:
		want := "http://localhost:8082?previewServer=8082&studio-version=1.2.0"
		if got != want {
			t.Fatalf("URL = %q, want %q", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("preview URL was not discovered")
	}
}

func TestListenLocalRejectsOccupiedPort(t *testing.T) {
	t.Parallel()

	bindErr := errors.New("address already in use")
	_, err := listenLocalWith(func(_, _ string) (net.Listener, error) {
		return nil, bindErr
	}, 8081)
	if err == nil || !errors.Is(err, bindErr) || !strings.Contains(err.Error(), "listen for documentation preview") {
		t.Fatalf("listenLocal() error = %v", err)
	}
}

func closeBody(t *testing.T, body io.Closer) {
	t.Helper()
	if err := body.Close(); err != nil {
		t.Errorf("close response body: %v", err)
	}
}
