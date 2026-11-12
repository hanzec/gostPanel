package main

import (
	"io"
	"os"
	"strings"
	"testing"

	"yagp/internal/buildinfo"
)

func TestCLIEntry(t *testing.T) {
	if err := run(nil); err == nil || !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("run with no arguments: %v", err)
	}
	for _, args := range [][]string{
		{"version", "unexpected"}, {"install"}, {"install-upgrade"}, {"deploy"}, {"self-upgrade"},
	} {
		if err := run(args); err == nil || !strings.Contains(err.Error(), "invalid yagp-depoly command") {
			t.Fatalf("run %v = %v, want an invalid command error", args, err)
		}
	}

	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	defer write.Close()
	oldArgs, oldOut := os.Args, os.Stdout
	os.Args, os.Stdout = []string{"yagp-depoly", "version"}, write
	defer func() { os.Args, os.Stdout = oldArgs, oldOut }()
	main()
	if err := write.Close(); err != nil {
		t.Fatal(err)
	}
	stdout, err := io.ReadAll(read)
	if err != nil || string(stdout) != buildinfo.Version+"\n" {
		t.Fatalf("version stdout = %q, err = %v", stdout, err)
	}
}
