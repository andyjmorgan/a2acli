// Package selfupdate upgrades the running a2acli binary in place from the
// project's GitHub releases, and maintains a small cache of the latest-seen
// version for the CLI's passive update notice. The mechanism mirrors dwvault's
// self-updater, adapted to goreleaser artifacts (per-platform .tar.gz archives
// plus a checksums.txt manifest).
//
// An upgrade fetches the archive for the current runtime, verifies its sha256
// against the manifest, extracts the binary, and atomically renames it over
// the live executable. PATH and shell profiles are never touched. While the
// repository is private, anonymous downloads 404 — the package then falls back
// to an authenticated `gh` CLI when available.
package selfupdate

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/andyjmorgan/a2acli/internal/config"
)

// defaultRepo is the GitHub "owner/name" hosting a2acli releases — the single
// source of truth shared with install.sh.
const defaultRepo = "andyjmorgan/a2acli"

const checksumsAsset = "checksums.txt"

// githubAPIBase is a var (not a const) only so tests can point Latest at an
// httptest server; production never changes it.
var githubAPIBase = "https://api.github.com"

// maxDownload caps a single asset read so a misconfigured A2ACLI_REPO can't
// exhaust memory. The archive is a few MB; 256 MB is comfortable headroom.
const maxDownload = 256 << 20

// Repo returns the release repo, honoring the A2ACLI_REPO override for parity
// with install.sh.
func Repo() string {
	if r := os.Getenv("A2ACLI_REPO"); r != "" {
		return r
	}
	return defaultRepo
}

// Release is the subset of the GitHub release payload we need.
type Release struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

// AssetName is the goreleaser archive name for this platform at a version,
// e.g. a2acli_0.1.0_linux_amd64.tar.gz.
func AssetName(tag string) string {
	return fmt.Sprintf("a2acli_%s_%s_%s.tar.gz", strings.TrimPrefix(tag, "v"), runtime.GOOS, runtime.GOARCH)
}

// Latest returns the newest published release. Anonymous first; falls back to
// an authenticated `gh` when the API 404s (private repo).
func Latest(ctx context.Context) (*Release, error) {
	url := fmt.Sprintf("%s/repos/%s/releases/latest", githubAPIBase, Repo())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode == http.StatusOK {
			var rel Release
			if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
				return nil, err
			}
			if rel.TagName == "" {
				return nil, errors.New("github releases: empty tag in response")
			}
			return &rel, nil
		}
		if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
			return nil, fmt.Errorf("github releases: %s", resp.Status)
		}
	}
	// Anonymous path failed — private repo or network policy. Try gh.
	if tag, gherr := ghLatestTag(ctx); gherr == nil {
		return &Release{TagName: tag}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("check releases: %w (and no authenticated gh available)", err)
	}
	return nil, fmt.Errorf("github releases: %s (private repo? authenticate gh and retry)", resp.Status)
}

func ghLatestTag(ctx context.Context) (string, error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return "", err
	}
	out, err := exec.CommandContext(ctx, "gh", "release", "view", "--repo", Repo(), "--json", "tagName", "--jq", ".tagName").Output()
	if err != nil {
		return "", err
	}
	tag := strings.TrimSpace(string(out))
	if tag == "" {
		return "", errors.New("gh returned an empty tag")
	}
	return tag, nil
}

// Compare orders two strict vMAJOR.MINOR.PATCH versions: -1 if a<b, 0 if
// equal, +1 if a>b. A version that doesn't parse (e.g. "dev") is treated as
// the lowest possible, so it never looks newer than a real release.
func Compare(a, b string) int {
	am, aok := parse(a)
	bm, bok := parse(b)
	switch {
	case !aok && !bok:
		return 0
	case !aok:
		return -1
	case !bok:
		return 1
	}
	for i := 0; i < 3; i++ {
		if am[i] != bm[i] {
			if am[i] < bm[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func parse(v string) ([3]int, bool) {
	var out [3]int
	s := strings.TrimPrefix(v, "v")
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// Download fetches this platform's release archive, verifies its sha256
// against checksums.txt, and returns the extracted a2acli binary bytes.
func Download(ctx context.Context, rel *Release) ([]byte, error) {
	name := AssetName(rel.TagName)

	archive, sums, err := fetchAssets(ctx, rel, name)
	if err != nil {
		return nil, err
	}

	want, err := sumFor(sums, name)
	if err != nil {
		return nil, err
	}
	got := sha256.Sum256(archive)
	if hex.EncodeToString(got[:]) != want {
		return nil, fmt.Errorf("checksum mismatch for %s (release may be corrupt)", name)
	}
	return extractBinary(archive)
}

// fetchAssets downloads the archive and manifest, preferring anonymous URLs
// and falling back to `gh release download` for private repos.
func fetchAssets(ctx context.Context, rel *Release, name string) (archive, sums []byte, err error) {
	binURL := assetURL(rel, name)
	sumsURL := assetURL(rel, checksumsAsset)
	if binURL != "" && sumsURL != "" {
		archive, aerr := fetch(ctx, binURL)
		if aerr == nil {
			sums, serr := fetch(ctx, sumsURL)
			if serr == nil {
				return archive, sums, nil
			}
		}
	}

	// gh fallback (also the primary path when Latest came via gh and the
	// release payload has no asset URLs).
	if _, lerr := exec.LookPath("gh"); lerr != nil {
		return nil, nil, fmt.Errorf("cannot download %s anonymously and gh is unavailable (private repo?)", name)
	}
	dir, err := os.MkdirTemp("", "a2acli-update-*")
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	cmd := exec.CommandContext(ctx, "gh", "release", "download", rel.TagName,
		"--repo", Repo(), "--dir", dir, "-p", name, "-p", checksumsAsset)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, nil, fmt.Errorf("gh release download: %w", err)
	}
	archive, err = os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // program-controlled temp path
	if err != nil {
		return nil, nil, err
	}
	sums, err = os.ReadFile(filepath.Join(dir, checksumsAsset)) //nolint:gosec // program-controlled temp path
	if err != nil {
		return nil, nil, err
	}
	return archive, sums, nil
}

func assetURL(rel *Release, name string) string {
	for _, a := range rel.Assets {
		if a.Name == name {
			return a.BrowserDownloadURL
		}
	}
	return ""
}

// sumFor extracts the hex sha256 for name from a "sha256  filename" manifest.
func sumFor(manifest []byte, name string) (string, error) {
	sc := bufio.NewScanner(bytes.NewReader(manifest))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) != 2 {
			continue
		}
		if strings.TrimPrefix(fields[1], "*") == name {
			sum := strings.ToLower(fields[0])
			if len(sum) != sha256.Size*2 {
				return "", fmt.Errorf("malformed checksum for %s in %s", name, checksumsAsset)
			}
			return sum, nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("%s not listed in %s", name, checksumsAsset)
}

// extractBinary pulls the a2acli file out of a goreleaser .tar.gz archive.
func extractBinary(archive []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("unpack archive: %w", err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil, errors.New("a2acli binary not found in release archive")
		}
		if err != nil {
			return nil, fmt.Errorf("unpack archive: %w", err)
		}
		if filepath.Base(hdr.Name) == "a2acli" && hdr.Typeflag == tar.TypeReg {
			return io.ReadAll(io.LimitReader(tr, maxDownload))
		}
	}
}

func fetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxDownload))
}

// executable is a var only so tests can point Apply at a temp file.
var executable = os.Executable

// Apply atomically replaces the running executable with bin: temp file in the
// same directory (rename stays atomic), chmod 0755, rename into place.
func Apply(bin []byte) (string, error) {
	exe, err := executable()
	if err != nil {
		return "", err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(exe)
	f, err := os.CreateTemp(dir, ".a2acli-update-*")
	if err != nil {
		return "", fmt.Errorf("cannot stage update in %s: %w", dir, err)
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }() // no-op once the rename succeeds
	if _, err := f.Write(bin); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(tmp, 0o755); err != nil { //nolint:gosec // staged binary must be executable
		return "", err
	}
	if err := os.Rename(tmp, exe); err != nil {
		return "", fmt.Errorf("cannot replace %s: %w", exe, err)
	}
	return exe, nil
}

// State is the cached result of the last passive update check.
type State struct {
	CheckedAt     time.Time `json:"checked_at"`
	LatestVersion string    `json:"latest_version"`
}

const stateFile = "update.json"

func statePath() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, stateFile), nil
}

// LoadState reads the update-check cache; absent or corrupt caches read as
// zero (the next check rewrites them).
func LoadState() (State, error) {
	var s State
	p, err := statePath()
	if err != nil {
		return s, err
	}
	b, err := os.ReadFile(p) //nolint:gosec // program-controlled cache path
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return State{}, nil
	}
	return s, nil
}

// SaveState writes the update-check cache atomically.
func SaveState(s State) error {
	p, err := statePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
