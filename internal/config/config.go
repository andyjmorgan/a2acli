// Package config manages the a2acli configuration file: agent aliases and
// their auth configuration. The main config never holds a raw secret; static
// secrets live in a separate 0600 secrets file, the OS keyring, or the
// environment (see internal/auth).
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/BurntSushi/toml"
)

// EnvConfigDir overrides the directory holding config.toml and secrets.toml.
// Used by tests and harnesses to isolate state.
const EnvConfigDir = "A2ACLI_CONFIG_DIR"

// Auth describes how to authenticate against one agent.
//
// Header/Value are the placement half: Value is a template in which "$secret"
// is replaced by the resolved secret ("$$" escapes a literal "$"). When empty,
// placement is derived from the Agent Card's security schemes.
//
// Secret/SecretCmd are the source half and are mutually exclusive. Secret
// names a secret resolved by walking env => keyring => secrets file.
// SecretCmd is executed and its stdout (trimmed) is the secret.
type Auth struct {
	// In is where the credential goes: "header" (default), "query" or
	// "cookie"; Header then names the header, query parameter or cookie.
	In        string `toml:"in,omitempty"`
	Header    string `toml:"header,omitempty"`
	Value     string `toml:"value,omitempty"`
	Secret    string `toml:"secret,omitempty"`
	SecretCmd string `toml:"secret_cmd,omitempty"`
}

// Agent is one aliased A2A server.
type Agent struct {
	URL  string `toml:"url"`
	Auth *Auth  `toml:"auth,omitempty"`
	// Extensions lists A2A extension URIs to activate for this agent (sent
	// in the A2A-Extensions header when the card advertises them).
	Extensions []string `toml:"extensions,omitempty"`
}

// Config is the root of config.toml.
type Config struct {
	Agents map[string]Agent `toml:"agents"`
}

func (a *Auth) Validate() error {
	if a == nil {
		return nil
	}
	if a.Secret != "" && a.SecretCmd != "" {
		return errors.New("auth: secret and secret_cmd are mutually exclusive")
	}
	switch a.In {
	case "", "header", "query", "cookie":
	default:
		return fmt.Errorf("auth: in must be header, query or cookie (got %q)", a.In)
	}
	if (a.In == "query" || a.In == "cookie") && a.Header == "" {
		return fmt.Errorf("auth: in = %q needs header = <parameter/cookie name>", a.In)
	}
	return nil
}

// Dir returns the config directory, honoring EnvConfigDir.
func Dir() (string, error) {
	if d := os.Getenv(EnvConfigDir); d != "" {
		return d, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate user config dir: %w", err)
	}
	return filepath.Join(base, "a2acli"), nil
}

func Path() (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "config.toml"), nil
}

// SecretsPath returns the path of the 0600 secrets file (the last stop of the
// secret walk). It lives beside config.toml and is never the main config.
func SecretsPath() (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "secrets.toml"), nil
}

// Load reads config.toml. A missing file yields an empty config.
func Load() (*Config, error) {
	p, err := Path()
	if err != nil {
		return nil, err
	}
	cfg := &Config{Agents: map[string]Agent{}}
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", p, err)
	}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", p, err)
	}
	if cfg.Agents == nil {
		cfg.Agents = map[string]Agent{}
	}
	for name, ag := range cfg.Agents {
		if err := ag.Auth.Validate(); err != nil {
			return nil, fmt.Errorf("agent %q: %w", name, err)
		}
	}
	return cfg, nil
}

// Save writes config.toml with owner-only permissions.
func (c *Config) Save() error {
	p, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	enc := toml.NewEncoder(f)
	if err := enc.Encode(c); err != nil {
		f.Close()
		return fmt.Errorf("encode %s: %w", p, err)
	}
	return f.Close()
}

// AgentNames returns alias names sorted for stable listings.
func (c *Config) AgentNames() []string {
	names := make([]string, 0, len(c.Agents))
	for n := range c.Agents {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// LoadSecretsFile reads secrets.toml (flat string map). Missing file is empty.
// It refuses group- or world-readable files rather than silently using them.
func LoadSecretsFile() (map[string]string, error) {
	p, err := SecretsPath()
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(p)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	if st.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s is not 0600 (mode %o); refusing to read", p, st.Mode().Perm())
	}
	secrets := map[string]string{}
	if _, err := toml.DecodeFile(p, &secrets); err != nil {
		return nil, fmt.Errorf("parse %s: %w", p, err)
	}
	return secrets, nil
}

// SaveSecretsFile writes secrets.toml with owner-only permissions.
func SaveSecretsFile(secrets map[string]string) error {
	p, err := SecretsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := toml.NewEncoder(f).Encode(secrets); err != nil {
		f.Close()
		return fmt.Errorf("encode %s: %w", p, err)
	}
	return f.Close()
}
