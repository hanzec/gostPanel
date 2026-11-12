package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"yagp/internal/deployjob"
)

const updatePath = "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

type updateRunner func(context.Context, deployjob.Request, io.Writer) error

func runManualUpdate(mode string, input io.Reader, output io.Writer, update updateRunner) error {
	request := deployjob.Request{Mode: mode}
	if mode == "compose" {
		if _, err := fmt.Fscan(io.LimitReader(input, 128), &request.Digest); err != nil || !request.ValidInput() {
			return errors.New("read a sha256 image digest from standard input")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 14*time.Minute)
	defer cancel()
	return update(ctx, request, output)
}

func submitUpdate(ctx context.Context, input io.Reader, mode, directory string, start func(context.Context, deployjob.Request) error) error {
	var request deployjob.Request
	data, err := io.ReadAll(io.LimitReader(input, 1025))
	if err != nil {
		return err
	}
	if len(data) > 1024 || json.Unmarshal(data, &request) != nil || !request.Valid() || request.Mode != mode {
		return errors.New("invalid update request")
	}
	if err := checkSelfUpgradePath(directory, false); err != nil {
		return err
	}
	dir := filepath.Join(directory, request.ID)
	if err := os.Mkdir(dir, 0750); err != nil {
		return err
	}
	if err := os.Chmod(dir, os.ModeSetgid|0750); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "request.json"), data, 0600); err != nil {
		return err
	}
	if err := writeStatus(dir, deployjob.Status{Mode: mode, State: "queued"}); err != nil {
		return err
	}

	if err := start(ctx, request); err != nil {
		// A timed out systemd client may already have submitted the unit.
		if ctx.Err() == nil {
			code := 1
			_ = writeStatus(dir, deployjob.Status{Mode: mode, State: "failed", ExitCode: &code})
		}
		return err
	}
	return nil
}

func startUpdateUnit(ctx context.Context, request deployjob.Request) error {
	// The transient unit keeps running while the webhook receiver is restarted.
	finish := "/usr/bin/env YAGP_UPDATE_PHASE=finish YAGP_UPDATE_JOB=" + request.ID + " " + deployjob.Executable + " " + request.Mode
	command := exec.CommandContext(ctx, "/usr/bin/systemd-run",
		"--no-block", "--collect", "--quiet", "--unit=yagp-update-"+request.ID,
		"--service-type=exec", "--property=RuntimeMaxSec=15min",
		"--property=LogNamespace=yagp-depoly", "--property=ExecStopPost="+finish,
		"--setenv=YAGP_UPDATE_PHASE=worker", "--setenv=YAGP_UPDATE_JOB="+request.ID,
		"--", deployjob.Executable, request.Mode)
	command.Env = []string{updatePath}
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("start update job: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func runUpdate(directory, id, mode string, update updateRunner) error {
	if !deployjob.ValidID(id) {
		return errors.New("invalid update job ID")
	}
	dir := filepath.Join(directory, id)
	data, err := os.ReadFile(filepath.Join(dir, "request.json"))
	if err != nil {
		return err
	}
	var request deployjob.Request
	if json.Unmarshal(data, &request) != nil || !request.Valid() || request.ID != id || request.Mode != mode {
		return errors.New("invalid stored update request")
	}
	logFile, err := os.OpenFile(filepath.Join(dir, "job.log"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0640)
	if err != nil {
		return err
	}
	defer logFile.Close()
	if err := logFile.Chmod(0640); err != nil {
		return err
	}
	output := io.MultiWriter(logFile, os.Stderr)
	log.SetOutput(output)
	defer log.SetOutput(os.Stderr)

	if err := writeStatus(dir, deployjob.Status{Mode: mode, State: "running"}); err != nil {
		return err
	}
	log.Printf("starting %s job %s", mode, id)
	ctx, cancel := context.WithTimeout(context.Background(), 14*time.Minute)
	defer cancel()
	if err := update(ctx, request, output); err != nil {
		log.Printf("update failed: %v", err)
		return err
	}
	log.Printf("update finished successfully")
	return nil
}

func runPublishedUpdate(ctx context.Context, request deployjob.Request, output io.Writer) error {
	switch request.Mode {
	case "depoly":
		return runNativeToolUpdate(ctx, selfUpgradeClient(), deployjob.Executable)
	case "compose":
		return runNativeComposeUpdate(ctx, request.Digest, output)
	default:
		return errors.New("invalid update mode")
	}
}

func finishUpdate(directory, id, mode string) error {
	if !deployjob.ValidID(id) {
		return errors.New("invalid update job ID")
	}
	state, code := "failed", 1
	if os.Getenv("EXIT_CODE") == "exited" {
		if value, err := strconv.Atoi(os.Getenv("EXIT_STATUS")); err == nil && value >= 0 && value <= 255 {
			code = value
		}
	}
	if os.Getenv("SERVICE_RESULT") == "success" && code == 0 {
		state = "succeeded"
	} else if code == 0 {
		code = 1
	}
	dir := filepath.Join(directory, id)
	return writeStatus(dir, deployjob.Status{Mode: mode, State: state, ExitCode: &code})
}

func writeStatus(dir string, status deployjob.Status) error {
	file, err := os.CreateTemp(dir, ".status-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	encodeErr := json.NewEncoder(file).Encode(status)
	if err := errors.Join(encodeErr, file.Chmod(0640), file.Close()); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(dir, "status.json"))
}
