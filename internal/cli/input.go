package cli

import (
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/andyjmorgan/a2acli/internal/client"
	"github.com/spf13/cobra"
)

// maxInlineFile caps --file attachments: parts are sent inline (base64 in
// JSON), so very large files belong behind a URL the agent can fetch.
const maxInlineFile = 10 << 20

// inputFlags are the shared message-content flags of send and stream.
type inputFlags struct {
	files  []string
	data   []string
	accept []string
}

func (f *inputFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringArrayVar(&f.files, "file", nil, "attach a file as an inline part (repeatable; media type from extension/content; max 10 MiB)")
	cmd.Flags().StringArrayVar(&f.data, "data", nil, "attach a structured-data part from a JSON value, or @path to read it from a file (repeatable)")
	cmd.Flags().StringArrayVar(&f.accept, "accept", nil, "accepted output media type, e.g. text/plain or application/json (repeatable; sent as acceptedOutputModes)")
}

// apply adds the extra parts and output modes to send options.
func (f *inputFlags) apply(o *client.SendOptions) error {
	for _, path := range f.files {
		p, err := filePart(path)
		if err != nil {
			return err
		}
		o.Parts = append(o.Parts, p)
	}
	for _, raw := range f.data {
		p, err := dataPart(raw)
		if err != nil {
			return err
		}
		o.Parts = append(o.Parts, p)
	}
	o.AcceptedOutputModes = f.accept
	return nil
}

func filePart(path string) (*a2a.Part, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("--file: %w", err)
	}
	if st.IsDir() {
		return nil, fmt.Errorf("--file %s: is a directory", path)
	}
	if st.Size() > maxInlineFile {
		return nil, fmt.Errorf("--file %s: %d bytes exceeds the %d-byte inline limit; host it and reference the URL in your message", path, st.Size(), maxInlineFile)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("--file: %w", err)
	}
	p := a2a.NewRawPart(b)
	p.Filename = filepath.Base(path)
	p.MediaType = mediaTypeOf(path, b)
	return p, nil
}

func mediaTypeOf(path string, content []byte) string {
	if t := mime.TypeByExtension(filepath.Ext(path)); t != "" {
		if mt, _, err := mime.ParseMediaType(t); err == nil {
			return mt
		}
	}
	mt, _, _ := mime.ParseMediaType(http.DetectContentType(content))
	return mt
}

func dataPart(raw string) (*a2a.Part, error) {
	src := []byte(raw)
	if len(raw) > 1 && raw[0] == '@' {
		b, err := os.ReadFile(raw[1:])
		if err != nil {
			return nil, fmt.Errorf("--data: %w", err)
		}
		src = b
	}
	var v any
	if err := json.Unmarshal(src, &v); err != nil {
		return nil, fmt.Errorf("--data: not valid JSON: %w", err)
	}
	return a2a.NewDataPart(v), nil
}
