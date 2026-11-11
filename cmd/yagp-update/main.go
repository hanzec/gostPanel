package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"yagp/internal/buildinfo"
	"yagp/internal/deployjob"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Print(err)
		var commandErr *exec.ExitError
		if errors.As(err, &commandErr) && commandErr.ExitCode() > 0 {
			os.Exit(commandErr.ExitCode())
		}
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) != 1 || (args[0] != "compose" && args[0] != "depoly") {
		return errors.New("usage: yagp-update compose | depoly")
	}
	mode := args[0]
	phase := os.Getenv("YAGP_UPDATE_PHASE")
	if phase == "version" && mode == "depoly" {
		fmt.Println(buildinfo.Version)
		return nil
	}
	if os.Geteuid() != 0 {
		return errors.New("yagp-update requires root")
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return err
	}
	if executable != deployjob.Executable {
		return errors.New("run the installed yagp-update")
	}
	if err := checkSelfUpgradeFile(executable); err != nil {
		return err
	}
	return runInstalledUpdate(executable, mode, phase)
}

func runInstalledUpdate(executable, mode, phase string) error {
	// A sudo caller can submit a job only. The worker phases run in a
	// root-created systemd unit with an explicit environment.
	if os.Getenv("SUDO_UID") != "" {
		if os.Getenv("SUDO_USER") == "yagp-deploy" {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			return submitUpdate(ctx, os.Stdin, mode, deployjob.Directory, startUpdateUnit)
		}
		phase = ""
	}
	switch phase {
	case "":
		return runManualUpdate(mode, os.Stdin, os.Stdout, runPublishedUpdate)
	case "worker":
		return runUpdate(deployjob.Directory, os.Getenv("YAGP_UPDATE_JOB"), mode, runPublishedUpdate)
	case "finish":
		return finishUpdate(deployjob.Directory, os.Getenv("YAGP_UPDATE_JOB"), mode)
	case "apply":
		if mode == "depoly" {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			// Retain the verified-archive entry point for the legacy upgrade script.
			return applyToolRelease(ctx, executable, os.Getenv("YAGP_UPDATE_RELEASE_TAG"), os.Stdin)
		}
	}
	return errors.New("invalid update phase")
}
