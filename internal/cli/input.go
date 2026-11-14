package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

// errTooLarge marks an input exceeding maxInlineFile.
var errTooLarge = errors.New("too large")

// readBounded reads a regular file through a hard size cap. It opens once
// and checks the opened descriptor (no stat/read race), refuses devices,
// FIFOs and directories (which could block or never end), and enforces the
// cap on the bytes actually read, so a file growing mid-read can't exceed it.
func readBounded(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: not a regular file", path)
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s: %w: exceeds the %d-byte inline limit; host it and reference the URL in your message", path, errTooLarge, limit)
	}
	return b, nil
}

func filePart(path string) (*a2a.Part, error) {
	b, err := readBounded(path, maxInlineFile)
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

// dataPart parses exactly one JSON value. Numbers are kept as json.Number
// so large integers (IDs, exact quantities) are sent verbatim instead of
// being rounded through float64.
func dataPart(raw string) (*a2a.Part, error) {
	src := []byte(raw)
	if len(raw) > 1 && raw[0] == '@' {
		b, err := readBounded(raw[1:], maxInlineFile)
		if err != nil {
			return nil, fmt.Errorf("--data: %w", err)
		}
		src = b
	}
	dec := json.NewDecoder(bytes.NewReader(src))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("--data: not valid JSON: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("--data: not valid JSON: expected exactly one value")
	}
	return a2a.NewDataPart(v), nil
}
