package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"

	"yagp/internal/buildinfo"
	"yagp/pkg/upgrade"
)

func runNativeToolUpdate(ctx context.Context, client *http.Client, executable string) error {
	if !upgrade.ValidateTag("daemon", buildinfo.Version) {
		return errors.New("tool self-upgrade requires a valid installed release version")
	}
	log.Print("checking the release cache for the latest tool version")
	tag, err := upgrade.CachedYAGPReleaseTag(ctx, client)
	if err != nil {
		return fmt.Errorf("query latest tool release: %w", err)
	}
	if !upgrade.IsNewer("daemon", buildinfo.Version, tag) {
		log.Printf("no newer tool release (installed %s, latest %s)", buildinfo.Version, tag)
		return nil
	}
	stage, err := os.MkdirTemp("", "yagp-tool-release-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)

	archive, err := downloadToolRelease(ctx, client, tag, stage)
	if err != nil {
		return err
	}
	log.Printf("verifying deployment tool release %s", tag)
	if err := verifyToolAttestation(archive, filepath.Join(stage, "yagp-depoly-attestations.jsonl")); err != nil {
		return fmt.Errorf("verify tool release: %w", err)
	}
	verified, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer verified.Close()
	return applyToolRelease(ctx, executable, tag, verified)
}

func downloadToolRelease(ctx context.Context, client *http.Client, tag, stage string) (string, error) {
	archive := "yagp-depoly-linux-" + runtime.GOARCH + ".tar.gz"
	base := upgrade.YAGPReleaseCache + "/releases/download/" + tag + "/"
	log.Printf("downloading deployment tools %s (%s)", tag, archive)
	for _, file := range []struct {
		name  string
		limit int64
	}{
		{archive, selfUpgradeMaxTar},
		{"yagp-depoly-attestations.jsonl", 2 << 20},
	} {
		if err := downloadSelfUpgradeFile(ctx, client, base+file.name, filepath.Join(stage, file.name), file.limit); err != nil {
			return "", fmt.Errorf("download %s: %w", file.name, err)
		}
	}
	return filepath.Join(stage, archive), nil
}
