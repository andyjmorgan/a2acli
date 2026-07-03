package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/andyjmorgan/a2acli/internal/config"
)

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v0.1.0", "v0.1.0", 0},
		{"v0.2.0", "v0.1.9", 1},
		{"v0.1.0", "v1.0.0", -1},
		{"dev", "v0.1.0", -1},
		{"v0.1.0", "dev", 1},
		{"dev", "dev", 0},
		{"v1.0.0", "v1.0.1", -1},
	}
	for _, c := range cases {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%q,%q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestAssetName(t *testing.T) {
	got := AssetName("v1.2.3")
	if !bytes.Contains([]byte(got), []byte("a2acli_1.2.3_")) || !bytes.HasSuffix([]byte(got), []byte(".tar.gz")) {
		t.Fatalf("AssetName = %q", got)
	}
}

// makeArchive builds a goreleaser-shaped tar.gz containing an a2acli "binary".
func makeArchive(t *testing.T, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	files := map[string][]byte{"README.md": []byte("readme"), "a2acli": content}
	for name, data := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// fakeRelease serves a GitHub-shaped release API plus its assets.
func fakeRelease(t *testing.T, tag string, binary []byte) *httptest.Server {
	t.Helper()
	asset := AssetName(tag)
	archive := makeArchive(t, binary)
	sum := sha256.Sum256(archive)
	manifest := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), asset)

	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/repos/", func(w http.ResponseWriter, r *http.Request) {
		rel := map[string]any{
			"tag_name": tag,
			"assets": []map[string]string{
				{"name": asset, "browser_download_url": srv.URL + "/dl/" + asset},
				{"name": checksumsAsset, "browser_download_url": srv.URL + "/dl/" + checksumsAsset},
			},
		}
		_ = json.NewEncoder(w).Encode(rel)
	})
	mux.HandleFunc("/dl/"+asset, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(archive) })
	mux.HandleFunc("/dl/"+checksumsAsset, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(manifest)) })
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestLatestDownloadApplyRoundTrip(t *testing.T) {
	t.Setenv(config.EnvConfigDir, t.TempDir())
	binary := []byte("#!/bin/sh\necho fake-new-binary\n")
	srv := fakeRelease(t, "v9.9.9", binary)

	oldBase := githubAPIBase
	githubAPIBase = srv.URL
	defer func() { githubAPIBase = oldBase }()

	rel, err := Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rel.TagName != "v9.9.9" {
		t.Fatalf("tag = %s", rel.TagName)
	}

	bin, err := Download(context.Background(), rel)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bin, binary) {
		t.Fatal("extracted binary differs from packed one")
	}

	// Apply against a fake "running executable".
	dir := t.TempDir()
	fake := filepath.Join(dir, "a2acli")
	if err := os.WriteFile(fake, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldExec := executable
	executable = func() (string, error) { return fake, nil }
	defer func() { executable = oldExec }()

	path, err := Apply(bin)
	if err != nil {
		t.Fatal(err)
	}
	if path != fake {
		t.Fatalf("replaced %s, want %s", path, fake)
	}
	got, _ := os.ReadFile(fake)
	if !bytes.Equal(got, binary) {
		t.Fatal("binary not replaced in place")
	}
	st, _ := os.Stat(fake)
	if st.Mode().Perm() != 0o755 {
		t.Fatalf("mode %o", st.Mode().Perm())
	}
}

func TestDownloadRejectsBadChecksum(t *testing.T) {
	tag := "v1.0.0"
	asset := AssetName(tag)
	archive := makeArchive(t, []byte("bin"))
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/dl/"+asset, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(archive) })
	mux.HandleFunc("/dl/"+checksumsAsset, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%064d  %s\n", 0, asset) // wrong sum
	})
	srv = httptest.NewServer(mux)
	defer srv.Close()

	rel := &Release{TagName: tag}
	rel.Assets = append(rel.Assets, struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	}{asset, srv.URL + "/dl/" + asset}, struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	}{checksumsAsset, srv.URL + "/dl/" + checksumsAsset})

	if _, err := Download(context.Background(), rel); err == nil {
		t.Fatal("bad checksum accepted")
	}
}

func TestStateRoundTrip(t *testing.T) {
	t.Setenv(config.EnvConfigDir, t.TempDir())
	s := State{CheckedAt: time.Now().Truncate(time.Second), LatestVersion: "v0.2.0"}
	if err := SaveState(s); err != nil {
		t.Fatal(err)
	}
	got, err := LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if got.LatestVersion != "v0.2.0" || !got.CheckedAt.Equal(s.CheckedAt) {
		t.Fatalf("got %+v", got)
	}
}

func TestLoadStateCorruptIsZero(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(config.EnvConfigDir, dir)
	if err := os.WriteFile(filepath.Join(dir, stateFile), []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadState()
	if err != nil || got.LatestVersion != "" {
		t.Fatalf("got %+v err=%v", got, err)
	}
}

func TestRepoOverride(t *testing.T) {
	t.Setenv("A2ACLI_REPO", "someone/else")
	if Repo() != "someone/else" {
		t.Fatal("A2ACLI_REPO override ignored")
	}
}
