package deployjob

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

const (
	Directory  = "/var/lib/yagp-update/jobs"
	Executable = "/usr/local/sbin/yagp-update"
	MaxLogRead = 64 << 10
)

var (
	jobID  = regexp.MustCompile(`^[0-9a-f]{32}$`)
	digest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

type Request struct {
	ID     string `json:"id"`
	Mode   string `json:"mode"`
	Digest string `json:"digest,omitempty"`
}

func ValidID(id string) bool {
	return jobID.MatchString(id)
}

func (r Request) Valid() bool {
	return ValidID(r.ID) && r.ValidInput()
}

func (r Request) ValidInput() bool {
	return (r.Mode == "compose" && digest.MatchString(r.Digest)) || (r.Mode == "depoly" && r.Digest == "")
}

type Status struct {
	Mode     string `json:"mode"`
	State    string `json:"state"`
	ExitCode *int   `json:"exit_code,omitempty"`
	Logs     string `json:"logs,omitempty"`
}

func ReadStatus(directory, id string) (Status, error) {
	var status Status
	if !ValidID(id) {
		return status, errors.New("invalid update job ID")
	}
	job := filepath.Join(directory, id)
	data, err := os.ReadFile(filepath.Join(job, "status.json"))
	if err != nil {
		return status, err
	}
	if err := json.Unmarshal(data, &status); err != nil {
		return status, err
	}

	logFile, err := os.Open(filepath.Join(job, "job.log"))
	if errors.Is(err, os.ErrNotExist) {
		return status, nil
	}
	if err != nil {
		return status, err
	}
	defer logFile.Close()
	info, err := logFile.Stat()
	if err != nil {
		return status, err
	}
	offset := max(0, info.Size()-MaxLogRead)
	if _, err := logFile.Seek(offset, io.SeekStart); err != nil {
		return status, err
	}
	data, err = io.ReadAll(io.LimitReader(logFile, MaxLogRead))
	status.Logs = string(data)
	return status, err
}
