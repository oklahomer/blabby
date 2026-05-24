// Command client launches the blabby TUI: a three-pane chat shell
// with a centred login modal as the initial screen. Run with
//
//	go run ./cmd/client --server http://localhost:8080
//
// The --server flag is the only required configuration; everything
// else (credentials, room selection, message text) is captured
// inside the TUI itself.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/oklahomer/blabby/cmd/client/internal/app"
)

// programRef is a lazy FrameSender that delegates Send to a
// *tea.Program once it is assigned. The Model needs a FrameSender
// at construction time, but *tea.Program needs a Model at
// construction time — programRef breaks that cycle by carrying a
// reassignable pointer through the Model and resolving it on first
// Send (after main wires the program back in).
//
// The pointer is read concurrently by the WebSocket read-loop
// goroutine and written by main on startup, so it lives behind an
// atomic.Pointer to avoid a data race.
type programRef struct {
	p atomic.Pointer[tea.Program]
}

func (r *programRef) set(p *tea.Program) { r.p.Store(p) }

func (r *programRef) Send(m tea.Msg) {
	if p := r.p.Load(); p != nil {
		p.Send(m)
	}
}

func main() {
	serverFlag := flag.String("server", "http://localhost:8080", "HTTP base URL of the blabby server (http:// or https://)")
	flag.Parse()

	parsed, err := parseServerURL(*serverFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid --server: %v\n", err)
		os.Exit(2)
	}

	// run() owns the signal-cancel defer; calling os.Exit from here
	// instead of inside run() lets that defer fire on error paths.
	if err := run(parsed); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(server *url.URL) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	ref := &programRef{}
	model := app.New(server, &http.Client{}).
		SetProgram(ref).
		SetContext(ctx).
		OpenLoginModal()
	program := tea.NewProgram(model, tea.WithAltScreen(), tea.WithContext(ctx))
	ref.set(program)

	if _, err := program.Run(); err != nil {
		return fmt.Errorf("client error: %w", err)
	}
	return nil
}

// parseServerURL validates the --server flag and returns the parsed
// URL. It rejects empty inputs, missing schemes, websocket schemes
// (ws/wss are not valid here — the client appends /login and /ws to
// the http base URL), and URLs without a host. Boundary parsing
// here means downstream packages can trust the URL is well-formed.
func parseServerURL(raw string) (*url.URL, error) {
	if raw == "" {
		return nil, fmt.Errorf("server URL must not be empty")
	}
	// url.Parse accepts "localhost:8080" by treating "localhost" as
	// the scheme — surface that as the friendlier missing-scheme
	// error before delegating to the parser.
	if !strings.Contains(raw, "://") {
		return nil, fmt.Errorf("missing scheme — expected http:// or https://")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	switch u.Scheme {
	case "http", "https":
	default:
		return nil, fmt.Errorf("unsupported scheme %q — expected http or https", u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("missing host")
	}
	return u, nil
}
