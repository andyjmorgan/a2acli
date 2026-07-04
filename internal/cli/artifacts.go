package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/andyjmorgan/a2acli/internal/client"
)

// maxArtifactFetch caps a single URL-part download.
const maxArtifactFetch = 512 << 20

// saveArtifacts writes every artifact part to dir and returns the file paths.
// Text parts become .txt, inline bytes are written verbatim, data parts
// become .json, URL parts are fetched with the agent's credentials.
func saveArtifacts(ctx context.Context, conn *client.Conn, arts []*a2a.Artifact, dir string) ([]string, error) {
	if len(arts) == 0 {
		return nil, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	var saved []string
	for ai, a := range arts {
		if a == nil {
			continue
		}
		base := sanitizeName(a.Name)
		if base == "" {
			base = sanitizeName(string(a.ID))
		}
		if base == "" {
			base = fmt.Sprintf("artifact-%d", ai)
		}
		var text strings.Builder
		filePart := 0
		for _, p := range a.Parts {
			if p == nil {
				continue
			}
			switch c := p.Content.(type) {
			case a2a.Text:
				text.WriteString(string(c))
			case a2a.Raw:
				path := filepath.Join(dir, partFilename(base, p.Filename, filePart, extForMedia(p.MediaType, ".bin")))
				if err := os.WriteFile(path, c, 0o644); err != nil {
					return saved, err
				}
				saved = append(saved, path)
				filePart++
			case a2a.Data:
				path := filepath.Join(dir, partFilename(base, p.Filename, filePart, ".json"))
				data, err := json.MarshalIndent(c.Value, "", "  ")
				if err != nil {
					return saved, fmt.Errorf("artifact %s data part: %w", a.ID, err)
				}
				if err := os.WriteFile(path, data, 0o644); err != nil {
					return saved, err
				}
				saved = append(saved, path)
				filePart++
			case a2a.URL:
				path := filepath.Join(dir, partFilename(base, p.Filename, filePart, extForMedia(p.MediaType, "")))
				if err := fetchTo(ctx, conn, string(c), path); err != nil {
					return saved, fmt.Errorf("artifact %s: fetch %s: %w", a.ID, c, err)
				}
				saved = append(saved, path)
				filePart++
			}
		}
		if text.Len() > 0 {
			path := filepath.Join(dir, base+".txt")
			if err := os.WriteFile(path, []byte(text.String()), 0o644); err != nil {
				return saved, err
			}
			saved = append(saved, path)
		}
	}
	return saved, nil
}

// fetchTo downloads a URL part using the connection's authenticated client.
func fetchTo(ctx context.Context, conn *client.Conn, url, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	hc := conn.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, io.LimitReader(resp.Body, maxArtifactFetch))
	return err
}

// partFilename picks a safe on-disk name for one artifact part.
func partFilename(base, declared string, idx int, ext string) string {
	if n := sanitizeName(declared); n != "" {
		return n
	}
	if idx == 0 {
		return base + ext
	}
	return fmt.Sprintf("%s-%d%s", base, idx, ext)
}

// sanitizeName strips path components and unsafe characters from a
// server-supplied filename — a remote agent must not choose where we write.
func sanitizeName(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	if name == "." || name == string(filepath.Separator) || name == ".." {
		return ""
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '-', r == '_', r == ' ':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := strings.Trim(b.String(), ". ")
	return out
}

// extForMedia maps common media types to file extensions.
func extForMedia(media, fallback string) string {
	switch {
	case strings.Contains(media, "json"):
		return ".json"
	case strings.HasPrefix(media, "text/"):
		return ".txt"
	case strings.Contains(media, "png"):
		return ".png"
	case strings.Contains(media, "pdf"):
		return ".pdf"
	}
	return fallback
}
