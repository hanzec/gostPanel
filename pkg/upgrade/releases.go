package upgrade

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const maxArchiveSize = 128 << 20

const YAGPReleaseCache = "https://release.hanzec-overseas.cn/yagp"

type Client struct {
	http *http.Client
}

func NewClient() *Client {
	return &Client{http: &http.Client{
		Timeout: 90 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			host := req.URL.Hostname()
			sameHost := req.URL.Host == via[0].URL.Host
			if len(via) >= 5 || req.URL.Scheme != "https" ||
				(!sameHost && host != "github.com" && host != "api.github.com" && host != "release-assets.githubusercontent.com") {
				return errors.New("unexpected GitHub release redirect")
			}
			return nil
		},
	}}
}

func repository(component string) string {
	switch component {
	case "gost":
		return "go-gost/gost"
	case "daemon":
		return "hanzec/yagp"
	default:
		return ""
	}
}

var (
	gostVersion   = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	daemonVersion = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)-[a-f0-9]{7}$`)
)

func ValidateTag(component, tag string) bool {
	_, ok := versionParts(component, tag)
	return ok && (component != "gost" || strings.HasPrefix(tag, "v"))
}

// IsNewer compares the numeric versions. The daemon's Git SHA identifies a build, not its order.
func IsNewer(component, current, target string) bool {
	old, oldOK := versionParts(component, current)
	next, nextOK := versionParts(component, target)
	if !oldOK || !nextOK {
		return false
	}
	for i := range old {
		if old[i] != next[i] {
			return old[i] < next[i]
		}
	}
	return false
}

func versionParts(component, version string) (parts [3]uint64, ok bool) {
	var match []string
	switch component {
	case "gost":
		match = gostVersion.FindStringSubmatch(version)
	case "daemon":
		match = daemonVersion.FindStringSubmatch(version)
	}
	if len(match) == 0 {
		return parts, false
	}
	for i := range parts {
		number, err := strconv.ParseUint(match[i+1], 10, 64)
		if err != nil {
			return parts, false
		}
		parts[i] = number
	}
	return parts, true
}

type release struct {
	Tag        string  `json:"tag_name"`
	CacheTag   string  `json:"tag"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	Assets     []asset `json:"assets"`
}

type asset struct {
	Name        string `json:"name"`
	Digest      string `json:"digest"`
	Size        int64  `json:"size"`
	DownloadURL string `json:"downloadUrl"`
}

// CachedYAGPReleaseTag reads the latest stable tag from the public release cache.
func CachedYAGPReleaseTag(ctx context.Context, client *http.Client) (string, error) {
	result, err := (&Client{http: client}).cachedYAGPRelease(ctx, "latest")
	if err != nil {
		return "", err
	}
	return result.Tag, nil
}

func (c *Client) cachedYAGPRelease(ctx context.Context, path string) (release, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return c.readRelease(ctx, "daemon", YAGPReleaseCache+"/"+path)
}

func preferYAGPReleaseCache(component string) bool {
	_, explicitProxy := os.LookupEnv("GH_PROXY")
	return component == "daemon" && !explicitProxy
}

func (c *Client) Latest(ctx context.Context, component string) (string, error) {
	if preferYAGPReleaseCache(component) {
		if result, err := c.cachedYAGPRelease(ctx, "latest"); err == nil {
			return result.Tag, nil
		}
	}
	release, err := c.release(ctx, component, "latest")
	if err != nil {
		return "", err
	}
	return release.Tag, nil
}

func (c *Client) LatestGostArchiveURL(ctx context.Context, arch string) (string, error) {
	tag, err := c.Latest(ctx, "gost")
	if err != nil {
		return "", err
	}
	return GostArchiveURL(tag, arch), nil
}

// GostArchiveURL formats the archive URL for a validated stable Gost tag.
func GostArchiveURL(tag, arch string) string {
	archive, _ := releaseNames("gost", tag, "", arch)
	return releaseArchiveURL("gost", tag, archive)
}

func (c *Client) release(ctx context.Context, component, endpoint string) (release, error) {
	repo := repository(component)
	if repo == "" {
		return release{}, errors.New("invalid upgrade component")
	}
	url := "https://api.github.com/repos/" + repo + "/releases/" + endpoint
	var proxyErr error
	// gh-fast accepts archives, but returns 403 for GitHub API URLs.
	if prefix := os.Getenv("GH_PROXY"); prefix != "" && prefix != ghFast {
		proxyCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
		result, err := c.readRelease(proxyCtx, component, prefix+url)
		cancel()
		if err == nil {
			return result, nil
		}
		proxyErr = fmt.Errorf("query release via GH_PROXY: %w", err)
	}
	result, err := c.readRelease(ctx, component, url)
	if err != nil && proxyErr != nil {
		return result, errors.Join(proxyErr, err)
	}
	return result, err
}

func (c *Client) readRelease(ctx context.Context, component, url string) (release, error) {
	var result release
	res, err := c.get(ctx, url, "application/json")
	if err != nil {
		return result, err
	}
	defer res.Body.Close()
	if err = json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&result); err != nil {
		return result, err
	}
	if strings.HasPrefix(url, YAGPReleaseCache+"/") {
		result.Tag = result.CacheTag
	}
	if result.Draft || result.Prerelease || !ValidateTag(component, result.Tag) {
		return result, errors.New("invalid stable release tag")
	}
	return result, nil
}

// Download checks the archive against its release digest before extracting the selected executable.
func (c *Client) Download(ctx context.Context, component, tag, variant string, dest io.Writer) error {
	if !ValidateTag(component, tag) || runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		return errors.New("unsupported platform or release tag")
	}
	archiveName, binaryName := releaseNames(component, tag, variant, runtime.GOARCH)
	url := releaseArchiveURL(component, tag, archiveName)
	if preferYAGPReleaseCache(component) {
		if cached, ok := c.cachedDaemonAsset(ctx, tag, archiveName); ok {
			return c.downloadAsset(ctx, cached.DownloadURL, cached, binaryName, dest, url)
		}
	}
	// A download proxy must not supply both the archive and its trusted digest.
	info, err := c.readRelease(ctx, component, "https://api.github.com/repos/"+repository(component)+"/releases/tags/"+tag)
	if err != nil {
		return err
	}
	if info.Tag != tag {
		return errors.New("release tag does not match the requested version")
	}
	metadata, ok := info.findAsset(archiveName)
	if !ok {
		return errors.New("release does not contain the required Linux archive")
	}
	sources := c.AssetURLs(ctx, url)
	return c.downloadAsset(ctx, sources[0], metadata, binaryName, dest, sources[1:]...)
}

func (c *Client) cachedDaemonAsset(ctx context.Context, tag, name string) (asset, bool) {
	info, err := c.cachedYAGPRelease(ctx, "releases/tag/"+tag)
	if err != nil || info.Tag != tag {
		return asset{}, false
	}
	metadata, ok := info.findAsset(name)
	url := YAGPReleaseCache + "/releases/download/" + tag + "/" + name
	return metadata, ok && metadata.Digest != "" && metadata.DownloadURL == url
}

func (r release) findAsset(name string) (asset, bool) {
	for _, metadata := range r.Assets {
		if metadata.Name == name {
			return metadata, true
		}
	}
	return asset{}, false
}

func releaseArchiveURL(component, tag, archive string) string {
	return "https://github.com/" + repository(component) + "/releases/download/" + tag + "/" + archive
}

func releaseNames(component, tag, variant, arch string) (archive, binary string) {
	if component == "gost" {
		return fmt.Sprintf("gost_%s_linux_%s.tar.gz", strings.TrimPrefix(tag, "v"), arch), "gost"
	}
	binary = "yagp-daemon-linux-" + arch
	archive = binary + ".tar.gz"
	if variant == "systemd" {
		binary = "yagp-daemon-systemd-linux-" + arch
	}
	return archive, binary
}

func (c *Client) downloadAsset(ctx context.Context, url string, asset asset, binary string, dest io.Writer, fallback ...string) error {
	digest, ok := strings.CutPrefix(asset.Digest, "sha256:")
	checksum, err := hex.DecodeString(digest)
	if !ok || err != nil || len(checksum) != sha256.Size || asset.Size <= 0 || asset.Size > maxArchiveSize {
		return errors.New("release asset has no usable SHA-256 digest or size")
	}
	file, err := os.CreateTemp("", "yagp-release-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if len(fallback) != 0 {
		mirrorCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		err = c.copyVerifiedAsset(mirrorCtx, url, asset.Size, checksum, file)
		cancel()
		if err != nil {
			log.Printf("release mirror download failed: %v; retrying GitHub directly", err)
			url = fallback[0]
		}
	}
	if len(fallback) == 0 || err != nil {
		if err = c.copyVerifiedAsset(ctx, url, asset.Size, checksum, file); err != nil {
			return err
		}
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	return extractBinary(file, binary, dest)
}

func (c *Client) copyVerifiedAsset(ctx context.Context, url string, size int64, checksum []byte, file *os.File) error {
	if err := file.Truncate(0); err != nil {
		return err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	res, err := c.get(ctx, url, "")
	if err != nil {
		return err
	}
	defer res.Body.Close()
	hash := sha256.New()
	length, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(res.Body, size+1))
	if err != nil {
		return err
	}
	if length != size || !bytes.Equal(hash.Sum(nil), checksum) {
		return errors.New("release archive SHA-256 or size does not match")
	}
	return nil
}

func extractBinary(source io.Reader, binary string, dest io.Writer) error {
	gz, err := gzip.NewReader(source)
	if err != nil {
		return err
	}
	defer gz.Close()
	archive := tar.NewReader(io.LimitReader(gz, 2*maxArchiveSize))
	for {
		entry, err := archive.Next()
		if errors.Is(err, io.EOF) {
			return errors.New("release archive does not contain the executable")
		}
		if err != nil {
			return err
		}
		if strings.TrimPrefix(entry.Name, "./") == binary && entry.Typeflag == tar.TypeReg && entry.Size > 0 && entry.Size <= maxArchiveSize {
			_, err = io.Copy(dest, archive)
			return err
		}
	}
}

func (c *Client) get(ctx context.Context, url, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "yagp-upgrade")
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		res.Body.Close()
		return nil, fmt.Errorf("release request returned HTTP %d", res.StatusCode)
	}
	return res, nil
}
