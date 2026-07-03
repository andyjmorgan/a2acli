// Package auth resolves agent credentials and injects them into requests.
//
// It has two orthogonal halves:
//
//   - Source: where the raw secret comes from. A named secret walks
//     env => OS keyring => 0600 secrets file; a command source executes a
//     program and takes its stdout. A per-invocation override can replace
//     either.
//   - Placement: which header carries it and with what surrounding text,
//     expressed as a value template containing "$secret".
package auth

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/andyjmorgan/a2acli/internal/config"
	"github.com/zalando/go-keyring"
)

// KeyringService is the service namespace used for OS keyring entries.
const KeyringService = "a2acli"

// EnvNoKeyring disables keyring lookups when set (useful on headless boxes
// where the Secret Service call can hang, and in tests).
const EnvNoKeyring = "A2ACLI_NO_KEYRING"

var envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// EnvVarName returns the normalized harness-injection env var for a secret
// name: A2ACLI_SECRET_<NAME> with non-alphanumerics mapped to underscores.
func EnvVarName(name string) string {
	up := strings.ToUpper(name)
	norm := strings.Map(func(r rune) rune {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		default:
			return '_'
		}
	}, up)
	return "A2ACLI_SECRET_" + norm
}

// WalkSecret resolves a named secret: env (verbatim name if it is a valid
// identifier, then A2ACLI_SECRET_<NAME>) => OS keyring => 0600 secrets file.
// The error on a full miss names everywhere it looked.
func WalkSecret(name string) (string, error) {
	var looked []string

	if envNameRe.MatchString(name) {
		looked = append(looked, "env $"+name)
		if v, ok := os.LookupEnv(name); ok && v != "" {
			return v, nil
		}
	}
	normVar := EnvVarName(name)
	looked = append(looked, "env $"+normVar)
	if v, ok := os.LookupEnv(normVar); ok && v != "" {
		return v, nil
	}

	if os.Getenv(EnvNoKeyring) == "" {
		v, err := keyringGet(name)
		switch {
		case err == nil && v != "":
			return v, nil
		case errors.Is(err, errKeyringTimeout):
			looked = append(looked, fmt.Sprintf("keyring (%s/%s — timed out; Secret Service not answering, set %s=1 to skip)", KeyringService, name, EnvNoKeyring))
		default:
			looked = append(looked, fmt.Sprintf("keyring (%s/%s)", KeyringService, name))
		}
	}

	secretsPath, _ := config.SecretsPath()
	looked = append(looked, "secrets file "+secretsPath)
	secrets, err := config.LoadSecretsFile()
	if err != nil {
		return "", fmt.Errorf("secret %q: reading secrets file: %w", name, err)
	}
	if v := secrets[name]; v != "" {
		return v, nil
	}

	return "", fmt.Errorf("secret %q not found; looked in: %s", name, strings.Join(looked, ", "))
}

// keyringTimeout bounds every OS keyring call: on machines without a working
// Secret Service the D-Bus request never returns, which would otherwise hang
// the whole CLI. The leaked goroutine is acceptable in a short-lived process.
const keyringTimeout = 3 * time.Second

var errKeyringTimeout = errors.New("keyring timed out")

func keyringGet(name string) (string, error) {
	type res struct {
		v   string
		err error
	}
	ch := make(chan res, 1)
	go func() {
		v, err := keyring.Get(KeyringService, name)
		ch <- res{v, err}
	}()
	select {
	case r := <-ch:
		return r.v, r.err
	case <-time.After(keyringTimeout):
		return "", errKeyringTimeout
	}
}

// StoreKeyring saves a named secret in the OS keyring.
func StoreKeyring(name, value string) error {
	ch := make(chan error, 1)
	go func() { ch <- keyring.Set(KeyringService, name, value) }()
	select {
	case err := <-ch:
		return err
	case <-time.After(keyringTimeout):
		return fmt.Errorf("%w after %s — no working Secret Service on this machine; use --file to store in the 0600 secrets file instead", errKeyringTimeout, keyringTimeout)
	}
}

// StoreSecretsFile saves a named secret in the 0600 secrets file.
func StoreSecretsFile(name, value string) error {
	secrets, err := config.LoadSecretsFile()
	if err != nil {
		return err
	}
	secrets[name] = value
	return config.SaveSecretsFile(secrets)
}

// Source yields a raw secret. Refreshable reports whether Invalidate can
// produce a new value (true only for command sources), which gates the
// 401 refresh-and-retry-once behavior.
type Source interface {
	Get() (string, error)
	Invalidate()
	Refreshable() bool
}

type nameSource struct{ name string }

func (s nameSource) Get() (string, error) { return WalkSecret(s.name) }
func (s nameSource) Invalidate()          {}
func (s nameSource) Refreshable() bool    { return false }

type literalSource struct{ value string }

func (s literalSource) Get() (string, error) { return s.value, nil }
func (s literalSource) Invalidate()          {}
func (s literalSource) Refreshable() bool    { return false }

// cmdSource executes a command via the shell and caches its stdout for the
// life of the process. Invalidate drops the cache so the next Get re-executes
// — this is how expired OAuth tokens are refreshed after a 401.
type cmdSource struct {
	cmdline string

	mu     sync.Mutex
	cached string
	valid  bool
}

func (s *cmdSource) Get() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.valid {
		return s.cached, nil
	}
	cmd := exec.Command("sh", "-c", s.cmdline)
	cmd.Stderr = os.Stderr // command diagnostics go to the user, never captured as secret
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("secret_cmd %q: %w", s.cmdline, err)
	}
	v := strings.TrimRight(string(out), "\r\n")
	if v == "" {
		return "", fmt.Errorf("secret_cmd %q produced no output", s.cmdline)
	}
	s.cached = v
	s.valid = true
	return v, nil
}

func (s *cmdSource) Invalidate() {
	s.mu.Lock()
	s.valid = false
	s.cached = ""
	s.mu.Unlock()
}

func (s *cmdSource) Refreshable() bool { return true }

// ParseOverride interprets the --secret flag: "cmd:<line>", "name:<name>",
// or a bare literal.
func ParseOverride(v string) Source {
	switch {
	case strings.HasPrefix(v, "cmd:"):
		return &cmdSource{cmdline: strings.TrimPrefix(v, "cmd:")}
	case strings.HasPrefix(v, "name:"):
		return nameSource{name: strings.TrimPrefix(v, "name:")}
	default:
		return literalSource{value: v}
	}
}

// SourceFor builds the secret source for an agent's configured auth,
// honoring an optional --secret override. Returns nil when the agent has no
// auth at all (anonymous server).
func SourceFor(a *config.Auth, override string) (Source, error) {
	if override != "" {
		return ParseOverride(override), nil
	}
	if a == nil {
		return nil, nil
	}
	if err := a.Validate(); err != nil {
		return nil, err
	}
	switch {
	case a.SecretCmd != "":
		return &cmdSource{cmdline: a.SecretCmd}, nil
	case a.Secret != "":
		return nameSource{name: a.Secret}, nil
	case a.Value != "" && !strings.Contains(a.Value, "$secret"):
		// Literal value template with no placeholder: the value IS the secret
		// material (testing escape hatch).
		return literalSource{value: ""}, nil
	default:
		return nil, nil
	}
}
