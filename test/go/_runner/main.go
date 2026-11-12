package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			os.Exit(exit.ExitCode())
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	sources := filepath.Join(root, "test", "go", "_packages")
	overlay := struct{ Replace map[string]string }{Replace: make(map[string]string)}
	// Go discovers same-package tests at the package path, including files supplied by an overlay.
	if err := filepath.WalkDir(sources, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		relative, err := filepath.Rel(sources, path)
		if err != nil {
			return err
		}
		overlay.Replace[filepath.Join(root, relative)] = path
		return nil
	}); err != nil {
		return fmt.Errorf("locate Go tests from the repository root: %w", err)
	}
	if len(overlay.Replace) == 0 {
		return errors.New("no Go tests found under test/go/_packages")
	}

	file, err := os.CreateTemp("", "yagp-test-overlay-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	encodeErr := json.NewEncoder(file).Encode(overlay)
	if err := errors.Join(encodeErr, file.Close()); err != nil {
		return err
	}

	if len(args) == 0 {
		args = []string{"./..."}
	}
	cmd := exec.Command("go", append([]string{"test", "-overlay=" + file.Name()}, args...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}
