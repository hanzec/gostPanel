package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"yagp/internal/buildinfo"
	"yagp/pkg/upgrade"
)

const (
	selfUpgradeRepo      = "hanzec/yagp"
	selfUpgradeWorkflow  = selfUpgradeRepo + "/.github/workflows/release.yml"
	selfUpgradeBundle    = "yagp-depoly-attestations.jsonl"
	selfUpgradeLockPath  = "/etc/yagp-depoly/deploy.lock"
	selfUpgradeMaxTar    = 64 << 20
	selfUpgradeMaxBundle = 2 << 20
)

var selfUpgradeGHVersion = regexp.MustCompile(`^gh version ([0-9]+)\.([0-9]+)\.[0-9]+(?: |$)`)

type selfUpgradeEnvironment struct {
	client      *http.Client
	gh          string
	systemctl   string
	lockPath    string
	credentials *syscall.Credential
}

func selfUpgrade(executable string) error {
	if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		return errors.New("tool self-upgrade requires Linux amd64 or arm64")
	}
	current := buildinfo.Version
	if !upgrade.ValidateTag("daemon", current) {
		return errors.New("tool self-upgrade requires an installed release version")
	}
	env, err := prepareSelfUpgrade(executable, selfUpgradeLockPath, findSelfUpgradeTool, user.Lookup)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	return env.install(ctx, executable, current)
}

func prepareSelfUpgrade(executable, lockPath string, findTool func(string) (string, error), lookupUser func(string) (*user.User, error)) (selfUpgradeEnvironment, error) {
	if err := checkSelfUpgradeFile(executable); err != nil {
		return selfUpgradeEnvironment{}, err
	}
	if err := checkSelfUpgradePath(filepath.Dir(lockPath), false); err != nil {
		return selfUpgradeEnvironment{}, err
	}
	gh, err := findTool("gh")
	if err != nil {
		return selfUpgradeEnvironment{}, err
	}
	systemctl, err := findTool("systemctl")
	if err != nil {
		return selfUpgradeEnvironment{}, err
	}
	credentials, err := selfUpgradeCredentials(lookupUser)
	if err != nil {
		return selfUpgradeEnvironment{}, err
	}
	return selfUpgradeEnvironment{
		client: selfUpgradeClient(), gh: gh, systemctl: systemctl,
		lockPath: lockPath, credentials: credentials,
	}, nil
}

func (env selfUpgradeEnvironment) install(ctx context.Context, executable, current string) error {
	tag, err := latestDeployRelease(ctx, env.client)
	if err != nil {
		return fmt.Errorf("query latest tool release: %w", err)
	}
	if !upgrade.IsNewer("daemon", current, tag) {
		log.Printf("no newer tool release (installed %s, latest %s)", current, tag)
		return nil
	}

	dir := filepath.Dir(executable)
	stage, err := os.MkdirTemp(dir, ".yagp-depoly-upgrade-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err := verifySelfUpgradeGHVersion(ctx, env.gh, stage); err != nil {
		return err
	}

	name := "yagp-depoly-linux-" + runtime.GOARCH
	archive := filepath.Join(stage, name+".tar.gz")
	bundle := filepath.Join(stage, selfUpgradeBundle)
	baseURL := "https://github.com/" + selfUpgradeRepo + "/releases/download/" + tag + "/"
	log.Printf("downloading tool release %s", tag)
	if err := downloadSelfUpgradeFile(ctx, env.client, baseURL+filepath.Base(archive), archive, selfUpgradeMaxTar); err != nil {
		return fmt.Errorf("download tool release: %w", err)
	}
	if err := downloadSelfUpgradeFile(ctx, env.client, baseURL+selfUpgradeBundle, bundle, selfUpgradeMaxBundle); err != nil {
		return fmt.Errorf("download tool attestation bundle: %w", err)
	}
	if err := verifySelfUpgradeAttestation(ctx, env.gh, stage, archive, bundle); err != nil {
		return err
	}
	candidate := filepath.Join(stage, name)
	if err := extractSelfUpgradeBinary(archive, name, candidate); err != nil {
		return fmt.Errorf("extract signed tool: %w", err)
	}

	// Keep the staging directory root-owned. Only the signed candidate needs to
	// be accessible to the unprivileged version check.
	if err := os.Chmod(stage, 0711); err != nil {
		return err
	}
	if err := checkSelfUpgradeVersion(ctx, candidate, tag, env.credentials); err != nil {
		return err
	}

	lock, err := acquireSelfUpgradeLock(ctx, env.lockPath)
	if err != nil {
		return err
	}
	defer lock.Close()
	// Another root invocation may have installed a release while downloading.
	if err := checkSelfUpgradeVersion(ctx, executable, current, env.credentials); err != nil {
		return fmt.Errorf("installed tool changed during self-upgrade: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := replaceSelfUpgradeBinary(executable, candidate, func(rollback bool) error {
		return restartSelfUpgradeService(env.systemctl, rollback)
	}); err != nil {
		return err
	}
	log.Printf("tool self-upgrade completed: %s → %s", current, tag)
	return nil
}

func selfUpgradeClient() *http.Client {
	return &http.Client{
		Timeout: 90 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			u := req.URL
			origin := via[0].URL.Host
			if len(via) >= 5 || u.Scheme != "https" || u.User != nil ||
				(u.Host != origin && (origin != "github.com" || u.Host != "release-assets.githubusercontent.com")) {
				return errors.New("unexpected tool release redirect")
			}
			return nil
		},
	}
}

func latestDeployRelease(ctx context.Context, client *http.Client) (string, error) {
	body, err := getSelfUpgradeFile(ctx, client, "https://api.github.com/repos/"+selfUpgradeRepo+"/releases/latest")
	if err != nil {
		return "", err
	}
	defer body.Close()
	var release struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if err := json.NewDecoder(io.LimitReader(body, 2<<20)).Decode(&release); err != nil {
		return "", err
	}
	if release.Draft || release.Prerelease || !upgrade.ValidateTag("daemon", release.Tag) {
		return "", errors.New("invalid stable tool release")
	}
	return release.Tag, nil
}

func getSelfUpgradeFile(ctx context.Context, client *http.Client, url string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GitHub returned %s", resp.Status)
	}
	return resp.Body, nil
}

func downloadSelfUpgradeFile(ctx context.Context, client *http.Client, url, target string, limit int64) error {
	body, err := getSelfUpgradeFile(ctx, client, url)
	if err != nil {
		return err
	}
	defer body.Close()
	file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	n, err := io.Copy(file, io.LimitReader(body, limit+1))
	err = errors.Join(err, file.Close())
	if err == nil && (n == 0 || n > limit) {
		err = errors.New("tool release file is empty or exceeds its size limit")
	}
	return err
}

func findSelfUpgradeTool(name string) (string, error) {
	for _, dir := range []string{"/usr/local/bin", "/usr/bin", "/bin"} {
		canonicalDir, err := filepath.EvalSymlinks(dir)
		if err != nil || checkSelfUpgradePath(canonicalDir, false) != nil {
			continue
		}
		candidate, err := filepath.EvalSymlinks(filepath.Join(canonicalDir, name))
		if err == nil && checkSelfUpgradeFile(candidate) == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("could not find a root-owned %s in a system executable directory", name)
}

func checkSelfUpgradeFile(path string) error {
	return checkSelfUpgradePath(path, true)
}

func checkSelfUpgradePath(path string, executable bool) error {
	if !filepath.IsAbs(path) {
		return errors.New("tool path must be absolute")
	}
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		isExecutable := current == path && executable
		if !ok || stat.Uid != 0 || info.Mode().Perm()&0022 != 0 ||
			(isExecutable && (!info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0)) ||
			(!isExecutable && !info.IsDir()) {
			return fmt.Errorf("tool path is not root-controlled: %s", current)
		}
		if current == "/" {
			return nil
		}
	}
}

func selfUpgradeCommand(ctx context.Context, workdir, executable string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir = workdir
	cmd.Env = []string{
		"PATH=/usr/bin:/bin:/usr/local/bin",
		"HOME=" + workdir,
		"GH_CONFIG_DIR=" + filepath.Join(workdir, ".gh"),
		"GH_PROMPT_DISABLED=1",
		"LC_ALL=C",
	}
	return cmd
}

func verifySelfUpgradeGHVersion(ctx context.Context, gh, stage string) error {
	output, err := selfUpgradeCommand(ctx, stage, gh, "--version").Output()
	if err != nil {
		return fmt.Errorf("check GitHub CLI version: %w", err)
	}
	matches := selfUpgradeGHVersion.FindSubmatch(output)
	if len(matches) != 0 {
		major, majorErr := strconv.ParseUint(string(matches[1]), 10, 32)
		minor, minorErr := strconv.ParseUint(string(matches[2]), 10, 32)
		if majorErr == nil && minorErr == nil && (major > 2 || (major == 2 && minor >= 102)) {
			return nil
		}
	}
	return errors.New("tool self-upgrade requires GitHub CLI 2.102.0 or newer")
}

func verifySelfUpgradeAttestation(ctx context.Context, gh, stage, archive, bundle string) error {
	cmd := selfUpgradeCommand(ctx, stage, gh, "attestation", "verify", archive,
		"--bundle", bundle, "--repo", selfUpgradeRepo, "--signer-workflow", selfUpgradeWorkflow,
		"--source-ref", "refs/heads/master", "--deny-self-hosted-runners")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("verify tool attestation: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func extractSelfUpgradeBinary(archive, name, target string) error {
	file, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer file.Close()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer compressed.Close()

	bounded := &io.LimitedReader{R: compressed, N: selfUpgradeMaxTar + 1}
	reader := tar.NewReader(bounded)
	found := false
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if header.Name != name {
			continue
		}
		if found || header.Typeflag != tar.TypeReg || header.Size <= 0 || header.Size >= bounded.N {
			return errors.New("signed archive contains an invalid tool executable entry")
		}
		found = true
		binary, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0755)
		if err != nil {
			return err
		}
		_, err = io.Copy(binary, reader)
		if err = errors.Join(err, binary.Chmod(0755), binary.Sync(), binary.Close()); err != nil {
			return err
		}
	}
	if !found || bounded.N == 0 {
		return errors.New("signed archive does not contain the expected bounded executable")
	}
	return nil
}

func selfUpgradeCredentials(lookupUser func(string) (*user.User, error)) (*syscall.Credential, error) {
	account, err := lookupUser("yagp-deploy")
	if err != nil {
		return nil, fmt.Errorf("find unprivileged tool account: %w", err)
	}
	uid, uidErr := strconv.ParseUint(account.Uid, 10, 32)
	gid, gidErr := strconv.ParseUint(account.Gid, 10, 32)
	if uidErr != nil || gidErr != nil || uid == 0 || gid == 0 {
		return nil, errors.New("invalid unprivileged tool account")
	}
	return &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}, nil
}

func checkSelfUpgradeVersion(ctx context.Context, executable, expected string, credentials *syscall.Credential) error {
	versionCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := selfUpgradeCommand(versionCtx, "/", executable, "version")
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: credentials}
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("check tool version: %w", err)
	}
	if strings.TrimSpace(string(output)) != expected {
		return fmt.Errorf("tool version does not match %s", expected)
	}
	return nil
}

func acquireSelfUpgradeLock(ctx context.Context, path string) (*os.File, error) {
	lock, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, fmt.Errorf("open deployment lock: %w", err)
	}
	info, err := lock.Stat()
	if err == nil {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != uint32(os.Geteuid()) || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 {
			err = errors.New("deployment lock is not root-controlled")
		}
	}
	if err == nil {
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for ctx.Err() == nil {
			err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
			if err == nil || (!errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR)) {
				break
			}
			select {
			case <-ctx.Done():
			case <-ticker.C:
			}
		}
		if ctx.Err() != nil {
			err = ctx.Err()
		}
	}
	if err != nil {
		lock.Close()
		return nil, fmt.Errorf("acquire deployment lock: %w", err)
	}
	return lock, nil
}

func replaceSelfUpgradeBinary(executable, candidate string, restart func(rollback bool) error) error {
	previous := filepath.Join(filepath.Dir(executable), ".yagp-depoly.previous")
	stagedPrevious := filepath.Join(filepath.Dir(candidate), "previous")
	if err := os.Link(executable, stagedPrevious); err != nil {
		return fmt.Errorf("back up installed tool: %w", err)
	}
	if err := os.Rename(stagedPrevious, previous); err != nil {
		return fmt.Errorf("save previous tool version: %w", err)
	}
	if err := os.Rename(candidate, executable); err != nil {
		return fmt.Errorf("replace installed tool: %w", err)
	}
	if err := restart(false); err != nil {
		restore := filepath.Join(filepath.Dir(candidate), "rollback")
		if restoreErr := os.Link(previous, restore); restoreErr != nil {
			return errors.Join(err, fmt.Errorf("stage tool rollback from %s: %w", previous, restoreErr))
		}
		if restoreErr := os.Rename(restore, executable); restoreErr != nil {
			return errors.Join(err, fmt.Errorf("restore previous tool from %s: %w", previous, restoreErr))
		}
		if rollbackErr := restart(true); rollbackErr != nil {
			return errors.Join(err, fmt.Errorf("restart previous tool: %w", rollbackErr))
		}
		return fmt.Errorf("tool self-upgrade failed and previous version was restored: %w", err)
	}
	return nil
}

func restartSelfUpgradeService(systemctl string, rollback bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	const deployUnit = "yagp-depoly.service"
	if rollback {
		// A bad release may have hit systemd's start rate limit.
		_ = selfUpgradeCommand(ctx, "/", systemctl, "reset-failed", deployUnit).Run()
	}
	cmd := selfUpgradeCommand(ctx, "/", systemctl, "restart", deployUnit)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("restart %s: %w: %s", deployUnit, err, strings.TrimSpace(string(output)))
	}
	if err := selfUpgradeCommand(ctx, "/", systemctl, "is-active", "--quiet", deployUnit).Run(); err != nil {
		return fmt.Errorf("%s did not remain active: %w", deployUnit, err)
	}
	return nil
}
