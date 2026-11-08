package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
)

func TestRunRejectsInvalidCLIAndConfiguration(t *testing.T) {
	ctx := context.Background()
	args := os.Args
	t.Cleanup(func() { os.Args = args })
	os.Args = []string{"yagp-daemon", "--version"}
	main()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "node.yaml")
	if err := os.WriteFile(configPath, []byte("node_id: 42\n"), 0600); err != nil {
		t.Fatal(err)
	}
	unknown := filepath.Join(dir, "unknown.yaml")
	if err := os.WriteFile(unknown, []byte("not_a_node_field: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "missing.yaml")
	for _, scenario := range []struct {
		name string
		args []string
		want string
	}{
		{"unknown configuration field", []string{"-c", unknown}, "not_a_node_field"},
		{"missing configuration file", []string{"-c", missing}, "missing.yaml"},
		{"invalid bridge config", []string{"-c", configPath}, "node ID and credentials are required"},
		{"enrollment flags", []string{"enroll"}, "requires --panel-address and --output"},
		{"prepare flags", []string{"prepare-gost"}, "requires --output"},
		{"ensure config missing", []string{"ensure-gost", "-c", missing}, "missing.yaml"},
		{"ensure API is loopback", []string{"ensure-gost", "-c", configPath}, "loopback Gost API address"},
		{"template config missing", []string{"nft-template", "-c", missing}, "missing.yaml"},
		{"template invalid report address", []string{"nft-template", "-c", configPath}, "missing address"},
		{"upgrade flags", []string{"upgrade-component", "--component", "gost"}, "requires --component and --tag"},
		{"upgrade config missing", []string{"upgrade-component", "--component", "gost", "--tag", "v3.3.3", "-c", missing}, "missing.yaml"},
		{"installed ID flag", []string{"--upgrade-installed"}, "requires --node-id"},
		{"installed config missing", []string{"--upgrade-installed", "--node-id", "42", "-c", missing}, "missing.yaml"},
		{"installed ID mismatch", []string{"--upgrade-installed", "--node-id", "41", "-c", configPath}, "does not match"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			if err := run(ctx, scenario.args); err == nil || !strings.Contains(err.Error(), scenario.want) {
				t.Fatalf("run(%v) = %v, want error containing %q", scenario.args, err, scenario.want)
			}
		})
	}
}

func TestEnrollmentCommandReadsTokenFromStdin(t *testing.T) {
	input := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(input, []byte("invalid token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(input)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	original := os.Stdin
	os.Stdin = file
	defer func() { os.Stdin = original }()
	output := t.TempDir()
	err = run(context.Background(), []string{"enroll", "--panel-address", "127.0.0.1:42", "--output", output})
	if err == nil || !strings.Contains(err.Error(), "invalid registration token") {
		t.Fatalf("invalid token read from stdin returned %v", err)
	}
	if entries, err := os.ReadDir(output); err != nil || len(entries) != 0 {
		t.Fatalf("invalid token left staged files: %v, %v", entries, err)
	}
}

func TestInstallGostBinaryPreservesInstalledVersionOnReadFailure(t *testing.T) {
	target := filepath.Join(t.TempDir(), "gost")
	if err := installGostBinary(target, strings.NewReader("working binary")); err != nil {
		t.Fatal(err)
	}
	interrupted := errors.New("download interrupted")
	if err := installGostBinary(target, iotest.ErrReader(interrupted)); !errors.Is(err, interrupted) {
		t.Fatalf("interrupted install = %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "working binary" {
		t.Fatalf("installed binary = %q, %v", got, err)
	}
	if err := installGostBinary(filepath.Join(target, "missing", "gost"), strings.NewReader("binary")); err == nil {
		t.Fatal("invalid installation directory accepted")
	}
}
