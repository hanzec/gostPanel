package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"yagp/pkg/nodegrpc"
)

func TestServerStartupAndSignalShutdown(t *testing.T) {
	if path := os.Getenv("YAGP_TEST_SERVER_CONFIG"); path != "" {
		earlySignals := make(chan os.Signal, 1)
		signal.Notify(earlySignals, syscall.SIGTERM)
		defer signal.Stop(earlySignals)
		flag.CommandLine = flag.NewFlagSet("yagp", flag.ExitOnError)
		os.Args = []string{"yagp", "-c", path}
		main()
		return
	}

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	reports, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer reports.Close()
	address := listener.Addr().String()
	reportAddress := reports.Addr().String()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	config := fmt.Sprintf("host: 127.0.0.1\nserver:\n  port: %q\n  mode: release\ndatabase:\n  type: sqlite\n  path: %q\nreport:\n  listen: %q\nlog:\n  level: info\n  output: stdout\n", address, filepath.Join(dir, "panel.db"), reportAddress)
	if err := os.WriteFile(path, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	args := []string{"-test.run=^TestServerStartupAndSignalShutdown$"}
	// A test subprocess has the same instrumented binary as its parent. Share
	// the Go test scratch directory so its actual startup coverage is collected.
	if coverage := flag.Lookup("test.gocoverdir"); coverage != nil && coverage.Value.String() != "" {
		args = append(args, "-test.gocoverdir="+coverage.Value.String())
	}
	child := exec.CommandContext(ctx, os.Args[0], args...)
	child.Dir = dir
	child.Env = append(os.Environ(), "YAGP_TEST_SERVER_CONFIG="+path, "YAGP_HOST=127.0.0.1", "YAGP_DATABASE_TYPE=sqlite", "YAGP_DATABASE_DSN=")
	var output bytes.Buffer
	child.Stdout, child.Stderr = &output, &output
	_ = listener.Close()
	_ = reports.Close()
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = child.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		_ = child.Process.Kill()
		<-done
	})

	httpClient := &http.Client{Timeout: 100 * time.Millisecond}
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+"/api/v1/health", nil)
		if err != nil {
			t.Fatal(err)
		}
		res, err := httpClient.Do(req)
		if err == nil {
			_ = res.Body.Close()
			if res.StatusCode == http.StatusOK {
				break
			}
		}
		select {
		case <-done:
			t.Fatalf("server exited before becoming ready: %v\n%s", waitErr, &output)
		case <-ctx.Done():
			t.Fatal("server did not become ready before the deadline")
		case <-time.After(25 * time.Millisecond):
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+reportAddress+nodegrpc.SessionPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := httpClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("node session without credentials = HTTP %d", res.StatusCode)
	}
	// HTTP can become ready while the background services are still starting.
	// Retry the signal until the server has installed its shutdown handler.
	for {
		if err := child.Process.Signal(syscall.SIGTERM); err != nil && err != os.ErrProcessDone {
			t.Fatal(err)
		}
		select {
		case <-done:
			if waitErr != nil || !bytes.Contains(output.Bytes(), []byte("yagp 正在关闭")) {
				t.Fatalf("graceful shutdown failed: %v\n%s", waitErr, &output)
			}
			return
		case <-ctx.Done():
			t.Fatal("server did not shut down after SIGTERM")
		case <-time.After(100 * time.Millisecond):
		}
	}
}
