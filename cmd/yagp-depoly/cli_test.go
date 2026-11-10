package main

import (
	"strings"
	"testing"
)

func TestRunRejectsUnexpectedPrivilegedArguments(t *testing.T) {
	for _, command := range []string{"deploy", "self-upgrade"} {
		t.Run(command, func(t *testing.T) {
			err := run([]string{command, "/tmp/other"})
			if err == nil || !strings.Contains(err.Error(), "takes no arguments") {
				t.Fatalf("run(%q) = %v, want privileged argument rejection", command, err)
			}
		})
	}
}

func TestRunVersion(t *testing.T) {
	if err := run([]string{"version"}); err != nil {
		t.Fatal(err)
	}
}
