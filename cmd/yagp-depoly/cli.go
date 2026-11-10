package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"

	"yagp/internal/buildinfo"
	"yagp/scripts"
)

const installedExecutable = "/usr/local/sbin/yagp-depoly"

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: yagp-depoly version | install <project-dir> [traefik] | install-upgrade [traefik]")
	}
	if args[0] == "version" && len(args) == 1 {
		fmt.Println(buildinfo.Version)
		return nil
	}

	executable, err := os.Executable()
	if err != nil {
		return err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return err
	}
	return dispatch(args, executable, os.Geteuid() == 0, executePrivileged)
}

func dispatch(args []string, executable string, root bool, execute func(string, ...string) error) error {
	switch args[0] {
	case "serve-panel", "serve-upgrade":
		return serve(args[0][len("serve-"):], executable, args[1:])
	case "deploy", "self-upgrade":
		if !root || len(args) != 1 || executable != installedExecutable {
			return errors.New("the installed deployment command requires root and takes no arguments")
		}
		if args[0] == "self-upgrade" {
			return execute(executable, "self-upgrade")
		}
		return execute(executable)
	case "install":
		if !root || len(args) < 2 || len(args) > 3 {
			return errors.New("usage: sudo yagp-depoly install <project-dir> [traefik]")
		}
		installArgs := []string{"install-webhook", args[1], executable}
		return execute(executable, append(installArgs, args[2:]...)...)
	case "install-upgrade":
		if !root || len(args) > 2 {
			return errors.New("usage: sudo yagp-depoly install-upgrade [traefik]")
		}
		return execute(executable, args...)
	default:
		return fmt.Errorf("invalid yagp-depoly command %q", args[0])
	}
}

func executePrivileged(executable string, args ...string) error {
	if len(args) > 0 && args[0] == "self-upgrade" {
		return selfUpgrade(executable)
	}
	return runInstaller(executable, args...)
}

func runInstaller(executable string, args ...string) error {
	command := exec.Command("/bin/bash", append([]string{"-p", "-c", scripts.DeployInstaller, executable}, args...)...)
	command.Env = []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	}
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	return command.Run()
}
