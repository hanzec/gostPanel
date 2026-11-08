package logger

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestLoggerOutputAndLevels(t *testing.T) {
	if err := Init(&Config{Level: "invalid"}); err == nil {
		t.Fatal("invalid level accepted")
	}
	old := log
	log = nil
	if err := Sync(); err != nil {
		t.Fatalf("sync uninitialized logger: %v", err)
	}
	t.Cleanup(func() { log = old })

	dir := t.TempDir()
	path := filepath.Join(dir, "logs", "app.log")
	output := path
	if runtime.GOOS == "windows" {
		// The production file sink stays open for the lifetime of the process.
		// Capture stdout with a file this test can close before Windows cleanup.
		path = filepath.Join(dir, "stdout.log")
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		original := os.Stdout
		os.Stdout = file
		t.Cleanup(func() { os.Stdout = original; _ = file.Close() })
		output = "stdout"
	}
	if err := Init(&Config{Level: "info", Format: "json", Output: output}); err != nil {
		t.Fatal(err)
	}
	Debug("excluded")
	Debugf("excluded %d", 2)
	Info("started")
	Infof("started %s", "node")
	Warn("retry")
	Warnf("retry %d", 2)
	Error("failed")
	Errorf("failed %s", "node")
	WithFields(map[string]any{"node_id": 42}).Info("registered")
	// Sync includes stdout, which some test harnesses expose as a pipe.
	_ = Sync()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var messages []string
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		var event struct {
			Message string `json:"msg"`
			NodeID  int    `json:"node_id"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		if event.Message == "registered" && event.NodeID != 42 {
			t.Fatalf("structured node ID = %d", event.NodeID)
		}
		messages = append(messages, event.Message)
	}
	if !slices.Equal(messages, []string{"started", "started node", "retry", "retry 2", "failed", "failed node", "registered"}) {
		t.Fatalf("messages = %v", messages)
	}
	if err := Init(&Config{Level: "debug", Format: "console", Output: "stdout"}); err != nil {
		t.Fatal(err)
	}
	if err := Init(&Config{Level: "warn", Output: filepath.Join(path, "invalid", "app.log")}); err == nil {
		t.Fatal("file used as a log directory")
	}
	if err := Init(&Config{Level: "warn", Output: filepath.Dir(path)}); err == nil {
		t.Fatal("directory used as a log file")
	}
}
