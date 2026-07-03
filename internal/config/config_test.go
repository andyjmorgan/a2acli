package config

import (
	"os"
	"path/filepath"
	"testing"
)

func setDir(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	t.Setenv(EnvConfigDir, d)
	return d
}

func TestLoadMissingIsEmpty(t *testing.T) {
	setDir(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Agents) != 0 {
		t.Fatalf("expected empty agents, got %v", cfg.Agents)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	d := setDir(t)
	cfg := &Config{Agents: map[string]Agent{
		"decliner": {
			URL: "https://decliner.example",
			Auth: &Auth{
				Header: "X-API-Key",
				Value:  "$secret",
				Secret: "decliner",
			},
		},
		"cmdagent": {
			URL:  "https://cmd.example",
			Auth: &Auth{SecretCmd: "echo tok"},
		},
		"noauth": {URL: "https://open.example"},
	}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}

	st, err := os.Stat(filepath.Join(d, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("config.toml mode = %o, want 0600", st.Mode().Perm())
	}

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	ag := got.Agents["decliner"]
	if ag.URL != "https://decliner.example" || ag.Auth == nil || ag.Auth.Secret != "decliner" || ag.Auth.Header != "X-API-Key" {
		t.Fatalf("round trip mismatch: %+v", ag)
	}
	if got.Agents["noauth"].Auth != nil {
		t.Fatalf("noauth agent grew an auth block: %+v", got.Agents["noauth"].Auth)
	}
}

func TestValidateMutuallyExclusive(t *testing.T) {
	setDir(t)
	cfg := &Config{Agents: map[string]Agent{
		"bad": {URL: "https://x", Auth: &Auth{Secret: "a", SecretCmd: "echo b"}},
	}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil {
		t.Fatal("expected mutual-exclusion error, got nil")
	}
}

func TestSecretsFilePermsEnforced(t *testing.T) {
	d := setDir(t)
	p := filepath.Join(d, "secrets.toml")
	if err := os.WriteFile(p, []byte(`foo = "bar"`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSecretsFile(); err == nil {
		t.Fatal("expected refusal of 0644 secrets file")
	}
	if err := os.Chmod(p, 0o600); err != nil {
		t.Fatal(err)
	}
	secrets, err := LoadSecretsFile()
	if err != nil {
		t.Fatal(err)
	}
	if secrets["foo"] != "bar" {
		t.Fatalf("secrets = %v", secrets)
	}
}

func TestSaveSecretsFile(t *testing.T) {
	d := setDir(t)
	if err := SaveSecretsFile(map[string]string{"k": "v"}); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(d, "secrets.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("secrets.toml mode = %o, want 0600", st.Mode().Perm())
	}
}
