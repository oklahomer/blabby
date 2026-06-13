// Command docs-preview serves local browser views of blabby's OpenAPI and
// AsyncAPI contracts. It builds temporary OpenAPI HTML with Redocly, starts the
// AsyncAPI CLI's read-only preview, and exposes both from one landing page.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	defaultDocsPort     = 8081
	defaultAsyncAPIPort = 8082
	previewStartTimeout = 2 * time.Minute
	shutdownTimeout     = 5 * time.Second
)

var previewURLPattern = regexp.MustCompile(
	`https?://(?:localhost|127\.0\.0\.1):[0-9]+\?previewServer=[A-Za-z0-9._~%!$&'()*+,;=:@/?+-]+`,
)

type port uint16

func newPort(name string, value int) (port, error) {
	if value < 1 || value > 65535 {
		return 0, fmt.Errorf("--%s must be between 1 and 65535", name)
	}
	return port(value), nil
}

func (p port) String() string {
	return strconv.FormatUint(uint64(p), 10)
}

type config struct {
	docsPort     port
	asyncAPIPort port
}

func main() {
	os.Exit(realMain())
}

func realMain() int {
	cfg, err := parseConfig(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func parseConfig(args []string) (config, error) {
	fs := flag.NewFlagSet("blabby-docs-preview", flag.ContinueOnError)
	docsPortValue := fs.Int("port", defaultDocsPort, "landing page port")
	asyncAPIPortValue := fs.Int("asyncapi-port", defaultAsyncAPIPort, "AsyncAPI preview port")
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	if fs.NArg() != 0 {
		return config{}, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}

	docsPort, err := newPort("port", *docsPortValue)
	if err != nil {
		return config{}, err
	}
	asyncAPIPort, err := newPort("asyncapi-port", *asyncAPIPortValue)
	if err != nil {
		return config{}, err
	}
	if docsPort == asyncAPIPort {
		return config{}, errors.New("--port and --asyncapi-port must be different")
	}

	return config{docsPort: docsPort, asyncAPIPort: asyncAPIPort}, nil
}

func run(ctx context.Context, cfg config) error {
	tempDir, err := os.MkdirTemp("", "blabby-docs-preview-*")
	if err != nil {
		return fmt.Errorf("create temporary directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(tempDir) }()

	openAPIPath := filepath.Join(tempDir, "openapi.html")
	if err := buildOpenAPIDocs(ctx, openAPIPath); err != nil {
		return err
	}
	openAPIHTML, err := os.ReadFile(openAPIPath)
	if err != nil {
		return fmt.Errorf("read generated OpenAPI documentation: %w", err)
	}

	listener, err := listenLocal(cfg.docsPort)
	if err != nil {
		return err
	}
	defer func() { _ = listener.Close() }()

	asyncPreview, err := startAsyncAPIPreview(cfg.asyncAPIPort)
	if err != nil {
		return err
	}
	defer asyncPreview.stop()

	asyncAPIURL, err := asyncPreview.awaitURL(ctx, previewStartTimeout)
	if err != nil {
		return err
	}

	landingURL := "http://localhost:" + cfg.docsPort.String()
	fmt.Printf("\nAPI contract preview: %s\n", landingURL)
	fmt.Println("Press Ctrl+C to stop both preview servers.")

	srv := &http.Server{
		Handler:           newPreviewHandler(openAPIHTML, asyncAPIURL),
		ReadHeaderTimeout: 5 * time.Second,
	}
	return serve(ctx, srv, listener, asyncPreview)
}

func buildOpenAPIDocs(ctx context.Context, outputPath string) error {
	args := []string{
		"--yes", "@redocly/cli@2", "build-docs", "api/openapi.yaml",
		"--output", outputPath,
		"--title", "blabby HTTP API",
		"--disableGoogleFont",
	}
	if err := runCommand(ctx, "npx", args...); err != nil {
		return fmt.Errorf("build OpenAPI documentation: %w", err)
	}
	return nil
}

func startAsyncAPIPreview(asyncAPIPort port) (*childProcess, error) {
	args := []string{
		"--yes", "@asyncapi/cli@6", "start", "preview", "api/asyncapi.yaml",
		"--port", asyncAPIPort.String(),
		"--noBrowser",
	}

	urlReady := make(chan string, 1)
	publisher := &urlPublisher{ready: urlReady}
	cmd := exec.Command("npx", args...)
	cmd.Env = append(os.Environ(), "SUPPRESS_NO_CONFIG_WARNING=true")
	cmd.Stdout = io.MultiWriter(os.Stdout, &previewURLFinder{publisher: publisher})
	cmd.Stderr = io.MultiWriter(os.Stderr, &previewURLFinder{publisher: publisher})
	configureProcess(cmd)

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start AsyncAPI preview: %w", err)
	}

	child := &childProcess{
		cmd:      cmd,
		done:     make(chan struct{}),
		urlReady: urlReady,
	}
	go func() {
		child.waitErr = cmd.Wait()
		close(child.done)
	}()
	return child, nil
}

func runCommand(ctx context.Context, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	configureProcess(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = terminateProcess(cmd)
		select {
		case <-done:
		case <-time.After(shutdownTimeout):
			_ = killProcess(cmd)
			<-done
		}
		return ctx.Err()
	}
}

func listenLocal(p port) (net.Listener, error) {
	return listenLocalWith(net.Listen, p)
}

func listenLocalWith(listen func(network, address string) (net.Listener, error), p port) (net.Listener, error) {
	listener, err := listen("tcp", net.JoinHostPort("127.0.0.1", p.String()))
	if err != nil {
		return nil, fmt.Errorf("listen for documentation preview on port %s: %w", p, err)
	}
	return listener, nil
}

type childProcess struct {
	cmd      *exec.Cmd
	done     chan struct{}
	urlReady <-chan string
	waitErr  error
	stopOnce sync.Once
}

func (p *childProcess) awaitURL(ctx context.Context, timeout time.Duration) (string, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case url := <-p.urlReady:
		return url, nil
	case <-p.done:
		if p.waitErr != nil {
			return "", fmt.Errorf("AsyncAPI preview stopped during startup: %w", p.waitErr)
		}
		return "", errors.New("AsyncAPI preview stopped before reporting its URL")
	case <-ctx.Done():
		return "", ctx.Err()
	case <-timer.C:
		return "", fmt.Errorf("AsyncAPI preview did not start within %s", timeout)
	}
}

func (p *childProcess) stop() {
	p.stopOnce.Do(func() {
		select {
		case <-p.done:
			return
		default:
		}

		_ = terminateProcess(p.cmd)
		select {
		case <-p.done:
		case <-time.After(shutdownTimeout):
			_ = killProcess(p.cmd)
			<-p.done
		}
	})
}

type urlPublisher struct {
	once  sync.Once
	ready chan<- string
}

func (p *urlPublisher) publish(url string) {
	p.once.Do(func() {
		p.ready <- url
	})
}

type previewURLFinder struct {
	buffer    bytes.Buffer
	publisher *urlPublisher
}

func (f *previewURLFinder) Write(data []byte) (int, error) {
	_, _ = f.buffer.Write(data)
	for {
		line, err := f.buffer.ReadString('\n')
		if err != nil {
			_, _ = f.buffer.WriteString(line)
			break
		}
		if !strings.Contains(line, "Open this URL in your web browser:") {
			continue
		}
		if match := previewURLPattern.FindString(line); match != "" {
			f.publisher.publish(match)
		}
	}
	return len(data), nil
}

func newPreviewHandler(openAPIHTML []byte, asyncAPIURL string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, landingPage)
	})
	mux.HandleFunc("GET /openapi", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(openAPIHTML)
	})
	mux.HandleFunc("GET /asyncapi", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, asyncAPIURL, http.StatusTemporaryRedirect)
	})
	return mux
}

func serve(ctx context.Context, srv *http.Server, listener net.Listener, asyncPreview *childProcess) error {
	serveErr := make(chan error, 1)
	go func() {
		err := srv.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serveErr <- err
	}()

	select {
	case err := <-serveErr:
		if err != nil {
			return fmt.Errorf("documentation preview server: %w", err)
		}
		return nil
	case <-asyncPreview.done:
		shutdownServer(srv)
		if asyncPreview.waitErr != nil {
			return fmt.Errorf("AsyncAPI preview stopped: %w", asyncPreview.waitErr)
		}
		return errors.New("AsyncAPI preview stopped unexpectedly")
	case <-ctx.Done():
		shutdownServer(srv)
		return nil
	}
}

func shutdownServer(srv *http.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

const landingPage = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>blabby API contracts</title>
  <style>
    :root { color-scheme: light dark; font-family: system-ui, sans-serif; }
    body { max-width: 760px; margin: 0 auto; padding: 3rem 1.5rem; }
    h1 { margin-bottom: .5rem; }
    .contracts { display: grid; gap: 1rem; margin-top: 2rem; }
    a { display: block; padding: 1.25rem; border: 1px solid #8886; border-radius: .5rem; color: inherit; text-decoration: none; }
    a:hover { border-color: #5875d8; }
    strong { display: block; margin-bottom: .35rem; font-size: 1.1rem; }
  </style>
</head>
<body>
  <main>
    <h1>blabby API contracts</h1>
    <p>Browse the client-facing HTTP and WebSocket contracts.</p>
    <div class="contracts">
      <a href="/openapi"><strong>HTTP API</strong>OpenAPI documentation for commands and queries.</a>
      <a href="/asyncapi"><strong>WebSocket API</strong>AsyncAPI documentation for the live event stream.</a>
    </div>
  </main>
</body>
</html>
`
