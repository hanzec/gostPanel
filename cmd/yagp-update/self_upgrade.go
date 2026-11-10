package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
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

func applyToolRelease(ctx context.Context, executable, tag string) error {
	if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		return errors.New("tool self-upgrade requires Linux amd64 or arm64")
	}
	current := buildinfo.Version
	if !upgrade.ValidateTag("daemon", current) || !upgrade.ValidateTag("daemon", tag) {
		return errors.New("tool self-upgrade requires valid installed and target release versions")
	}
	if !upgrade.IsNewer("daemon", current, tag) {
		log.Printf("no newer tool release (installed %s, latest %s)", current, tag)
		return nil
	}
	receiver := filepath.Join(filepath.Dir(executable), "yagp-depoly")
	if err := checkSelfUpgradeFile(receiver); err != nil {
		return err
	}
	env, err := prepareSelfUpgrade(executable, selfUpgradeLockPath, findSelfUpgradeTool, user.Lookup)
	if err != nil {
		return err
	}
	return env.install(ctx, executable, receiver, current, tag)
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

func (env selfUpgradeEnvironment) install(ctx context.Context, executable, receiver, current, tag string) error {
	dir := filepath.Dir(executable)
	stage, err := os.MkdirTemp(dir, ".yagp-update-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err := verifySelfUpgradeGHVersion(ctx, env.gh, stage); err != nil {
		return err
	}

	receiverName := "yagp-depoly-linux-" + runtime.GOARCH
	updaterName := "yagp-update-linux-" + runtime.GOARCH
	archive := filepath.Join(stage, receiverName+".tar.gz")
	bundle := filepath.Join(stage, selfUpgradeBundle)
	log.Printf("preparing tool self-upgrade %s → %s", current, tag)
	if err := env.downloadReleaseFile(ctx, tag, archive, selfUpgradeMaxTar); err != nil {
		return fmt.Errorf("download tool release: %w", err)
	}
	if err := env.downloadReleaseFile(ctx, tag, bundle, selfUpgradeMaxBundle); err != nil {
		return fmt.Errorf("download tool attestation bundle: %w", err)
	}
	log.Printf("verifying the attestation for tool release %s", tag)
	if err := verifySelfUpgradeAttestation(ctx, env.gh, stage, archive, bundle); err != nil {
		return err
	}

	newUpdater := filepath.Join(stage, updaterName)
	newReceiver := filepath.Join(stage, receiverName)
	entries := map[string]string{updaterName: newUpdater, receiverName: newReceiver}
	if err := extractSelfUpgradeBinaries(archive, entries); err != nil {
		return fmt.Errorf("extract signed tools: %w", err)
	}

	// Keep the staging directory root-owned while allowing the unprivileged
	// version checks to execute the signed candidates.
	if err := os.Chmod(stage, 0711); err != nil {
		return err
	}
	if err := checkSelfUpgradeVersion(ctx, newUpdater, tag, env.credentials, true); err != nil {
		return err
	}
	if err := checkSelfUpgradeVersion(ctx, newReceiver, tag, env.credentials, false); err != nil {
		return err
	}

	lock, err := acquireSelfUpgradeLock(ctx, env.lockPath)
	if err != nil {
		return err
	}
	defer lock.Close()
	// Another root invocation may have installed a release while downloading.
	if err := checkSelfUpgradeVersion(ctx, executable, current, env.credentials, true); err != nil {
		return fmt.Errorf("installed updater changed during self-upgrade: %w", err)
	}
	if err := checkSelfUpgradeVersion(ctx, receiver, current, env.credentials, false); err != nil {
		return fmt.Errorf("installed receiver changed during self-upgrade: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	log.Printf("installing both tools from release %s and restarting yagp-depoly.service", tag)
	binaries := []binaryReplacement{
		{target: executable, candidate: newUpdater},
		{target: receiver, candidate: newReceiver},
	}
	if err := replaceSelfUpgradeBinaries(binaries, func(rollback bool) error {
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
			if len(via) >= 5 || u.Scheme != "https" || u.User != nil || u.Host != via[0].URL.Host {
				return errors.New("unexpected tool release redirect")
			}
			return nil
		},
	}
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
		return nil, fmt.Errorf("tool release request returned %s", resp.Status)
	}
	return resp.Body, nil
}

func (env selfUpgradeEnvironment) downloadReleaseFile(ctx context.Context, tag, target string, limit int64) error {
	asset := filepath.Base(target)
	log.Printf("downloading %s from the tool release cache", asset)
	downloadCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	return downloadSelfUpgradeFile(downloadCtx, env.client, upgrade.YAGPReleaseCache+"/releases/download/"+tag+"/"+asset, target, limit)
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
	if err != nil {
		return errors.Join(err, os.Remove(target))
	}
	return nil
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

func extractSelfUpgradeBinaries(archive string, targets map[string]string) error {
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
	found := make(map[string]bool, len(targets))
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		target, wanted := targets[header.Name]
		if !wanted {
			continue
		}
		if found[header.Name] || header.Typeflag != tar.TypeReg || header.Size <= 0 || header.Size >= bounded.N {
			return fmt.Errorf("signed archive contains an invalid executable entry for %s", header.Name)
		}
		found[header.Name] = true
		binary, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0755)
		if err != nil {
			return err
		}
		_, err = io.Copy(binary, reader)
		if err = errors.Join(err, binary.Chmod(0755), binary.Sync(), binary.Close()); err != nil {
			return err
		}
	}
	if len(found) != len(targets) || bounded.N == 0 {
		return errors.New("signed archive does not contain the expected bounded executables")
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

func checkSelfUpgradeVersion(ctx context.Context, executable, expected string, credentials *syscall.Credential, updater bool) error {
	versionCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	arg := "version"
	if updater {
		arg = "depoly"
	}
	cmd := selfUpgradeCommand(versionCtx, "/", executable, arg)
	if updater {
		cmd.Env = append(cmd.Env, "YAGP_UPDATE_PHASE=version")
	}
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

type binaryReplacement struct {
	target    string
	candidate string
}

func (binary binaryReplacement) previous() string {
	return filepath.Join(filepath.Dir(binary.target), "."+filepath.Base(binary.target)+".previous")
}

func replaceSelfUpgradeBinaries(binaries []binaryReplacement, restart func(rollback bool) error) error {
	for _, binary := range binaries {
		backup := filepath.Join(filepath.Dir(binary.candidate), filepath.Base(binary.target)+".previous")
		if err := os.Link(binary.target, backup); err != nil {
			return fmt.Errorf("back up installed tool %s: %w", binary.target, err)
		}
		if err := os.Rename(backup, binary.previous()); err != nil {
			return fmt.Errorf("save previous tool version %s: %w", binary.target, err)
		}
	}
	for i, binary := range binaries {
		if err := os.Rename(binary.candidate, binary.target); err != nil {
			return errors.Join(fmt.Errorf("replace installed tool %s: %w", binary.target, err),
				restoreSelfUpgradeBinaries(binaries[:i]))
		}
	}
	if err := restart(false); err != nil {
		log.Print("tool restart failed, restoring both previous versions")
		if restoreErr := restoreSelfUpgradeBinaries(binaries); restoreErr != nil {
			return errors.Join(err, restoreErr)
		}
		if rollbackErr := restart(true); rollbackErr != nil {
			return errors.Join(err, fmt.Errorf("restart previous tool: %w", rollbackErr))
		}
		return fmt.Errorf("tool self-upgrade failed and both previous versions were restored: %w", err)
	}
	return nil
}

func restoreSelfUpgradeBinaries(binaries []binaryReplacement) error {
	var restoreErr error
	for _, binary := range binaries {
		restore := filepath.Join(filepath.Dir(binary.candidate), filepath.Base(binary.target)+".rollback")
		if err := os.Link(binary.previous(), restore); err != nil {
			restoreErr = errors.Join(restoreErr, fmt.Errorf("stage tool rollback from %s: %w", binary.previous(), err))
			continue
		}
		if err := os.Rename(restore, binary.target); err != nil {
			restoreErr = errors.Join(restoreErr, fmt.Errorf("restore previous tool from %s: %w", binary.previous(), err))
		}
	}
	return restoreErr
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
