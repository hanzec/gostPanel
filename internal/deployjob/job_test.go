package deployjob

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testJobID = "0123456789abcdef0123456789abcdef"

func TestRequestValid(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		name    string
		request Request
		valid   bool
	}{
		{"compose", Request{ID: testJobID, Mode: "compose", Digest: digest}, true},
		{"depoly", Request{ID: testJobID, Mode: "depoly"}, true},
		{"unknown mode", Request{ID: testJobID, Mode: "deploy"}, false},
		{"missing compose digest", Request{ID: testJobID, Mode: "compose"}, false},
		{"unexpected depoly digest", Request{ID: testJobID, Mode: "depoly", Digest: digest}, false},
		{"invalid ID", Request{ID: "../" + testJobID, Mode: "depoly"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if valid := tc.request.Valid(); valid != tc.valid {
				t.Errorf("Request.Valid() = %t, want %t", valid, tc.valid)
			}
		})
	}
}

func TestReadStatus(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "jobs")
	job := filepath.Join(directory, testJobID)
	if err := os.MkdirAll(job, 0750); err != nil {
		t.Fatal(err)
	}
	statusPath := filepath.Join(job, "status.json")
	if _, err := ReadStatus(directory, testJobID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing status error = %v, want os.ErrNotExist", err)
	}
	if err := os.WriteFile(statusPath, []byte(`{"mode":"depoly","state":"queued"}`), 0640); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadStatus(directory, "../jobs/"+testJobID); err == nil {
		t.Fatal("accepted a job ID that traverses the job directory")
	}
	status, err := ReadStatus(directory, testJobID)
	if err != nil || status.Mode != "depoly" || status.State != "queued" || status.ExitCode != nil || status.Logs != "" {
		t.Fatalf("queued job status = %+v, err = %v", status, err)
	}

	if err := os.WriteFile(statusPath, []byte(`{"mode":"depoly","state":"running"}`), 0640); err != nil {
		t.Fatal(err)
	}
	wantTail := strings.Repeat("b", MaxLogRead-1) + "\n"
	if err := os.WriteFile(filepath.Join(job, "job.log"), []byte("older output\n"+wantTail), 0640); err != nil {
		t.Fatal(err)
	}
	status, err = ReadStatus(directory, testJobID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Mode != "depoly" || status.State != "running" || status.ExitCode != nil || status.Logs != wantTail {
		t.Fatalf("running job mode = %q, state = %q, exit code = %v, log size = %d, want the running job and its latest %d log bytes",
			status.Mode, status.State, status.ExitCode, len(status.Logs), MaxLogRead)
	}
	if err := os.WriteFile(statusPath, []byte(`{"state":`), 0640); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadStatus(directory, testJobID); err == nil {
		t.Fatal("accepted invalid status JSON")
	}
}
