package cli

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/andyjmorgan/a2acli/internal/client"
)

func TestReadBoundedRejectsNonRegularAndOversized(t *testing.T) {
	if runtime.GOOS != "windows" {
		if _, err := readBounded("/dev/zero", 16); err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("/dev/zero must be refused, got %v", err)
		}
	}
	if _, err := readBounded(t.TempDir(), 16); err == nil {
		t.Fatal("directory accepted")
	}
	big := filepath.Join(t.TempDir(), "big")
	if err := os.WriteFile(big, make([]byte, 17), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readBounded(big, 16); !errors.Is(err, errTooLarge) {
		t.Fatalf("oversized file: %v", err)
	}
	b, err := readBounded(big, 17)
	if err != nil || len(b) != 17 {
		t.Fatalf("exact-limit file: len=%d err=%v", len(b), err)
	}
}

func TestDataPartPreservesLargeIntegers(t *testing.T) {
	p, err := dataPart(`{"id":9007199254740993,"q":0.1}`)
	if err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(p.Content.(a2a.Data).Value)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "9007199254740993") {
		t.Fatalf("integer rounded: %s", out)
	}
}

func TestDataPartRejectsTrailingData(t *testing.T) {
	for _, in := range []string{`{"a":1} {"b":2}`, `1 2`, `{"a":1} junk`} {
		if _, err := dataPart(in); err == nil {
			t.Errorf("accepted %q", in)
		}
	}
}

func TestIsDefinitive(t *testing.T) {
	for _, e := range []error{a2a.ErrTaskNotFound, a2a.ErrUnauthenticated, a2a.ErrVersionNotSupported} {
		if !isDefinitive(a2a.NewError(e, "x")) {
			t.Errorf("%v should be definitive", e)
		}
	}
	for _, e := range []error{a2a.NewError(a2a.ErrInternalError, "db down"), a2a.NewError(a2a.ErrServerError, "x"), errors.New("connection reset")} {
		if isDefinitive(e) {
			t.Errorf("%v must be retried", e)
		}
	}
}

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"plain":          "plain",
		"http://h:1/a2a": "http://h:1/a2a",
		"ctx a":          "'ctx a'",
		"it's":           `'it'\''s'`,
		"":               "''",
		"$(rm -rf ~)":    "'$(rm -rf ~)'",
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestFetchToRejectsOversizedArtifact(t *testing.T) {
	old := maxArtifactFetch
	maxArtifactFetch = 8
	defer func() { maxArtifactFetch = old }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(strings.Repeat("x", int(r.URL.Query().Get("n")[0]-'0'))))
	}))
	defer srv.Close()
	conn := &client.Conn{HTTP: srv.Client()}
	dir := t.TempDir()

	path := filepath.Join(dir, "big.bin")
	if err := fetchTo(t.Context(), conn, srv.URL+"?n=9", path); err == nil {
		t.Fatal("oversized artifact reported as saved")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("partial file left behind: %v", err)
	}
	ok := filepath.Join(dir, "ok.bin")
	if err := fetchTo(t.Context(), conn, srv.URL+"?n=8", ok); err != nil {
		t.Fatalf("exact-limit artifact: %v", err)
	}
}
