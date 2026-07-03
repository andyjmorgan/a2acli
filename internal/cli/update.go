package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/andyjmorgan/a2acli/internal/selfupdate"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// checkInterval is how long a passive update check stays fresh.
const checkInterval = 24 * time.Hour

// EnvNoUpdateCheck disables the passive update notice entirely.
const EnvNoUpdateCheck = "A2ACLI_NO_UPDATE_CHECK"

// cliVersion is stamped by Execute from main's ldflags value.
var cliVersion = "dev"

func newUpdateCmd() *cobra.Command {
	var check, force bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Upgrade a2acli to the latest release (in place)",
		Long: `Download the latest a2acli release for this platform, verify its checksum,
and replace the running binary in place. PATH is not modified — the binary is
updated wherever it already lives. While the repository is private, an
authenticated gh CLI is used automatically when anonymous downloads fail.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runUpdate(force, check)
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "report whether an update is available, without installing")
	cmd.Flags().BoolVar(&force, "force", false, "reinstall the latest release even if already current")
	return cmd
}

func runUpdate(force, checkOnly bool) error {
	if cliVersion == "dev" && !force {
		fmt.Fprintln(os.Stderr, "a2acli: built from source (version \"dev\"); nothing to update — use --force to install the latest release anyway")
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	rel, err := selfupdate.Latest(ctx)
	if err != nil {
		return fmt.Errorf("check for updates: %w", err)
	}

	// Refresh the passive-notice cache while we have a fresh answer.
	_ = selfupdate.SaveState(selfupdate.State{CheckedAt: time.Now(), LatestVersion: rel.TagName})

	newer := selfupdate.Compare(rel.TagName, cliVersion) > 0
	if checkOnly {
		if newer {
			fmt.Fprintf(os.Stderr, "a2acli %s is available (you have %s) — run `a2acli update` to upgrade\n", rel.TagName, cliVersion)
		} else {
			fmt.Fprintf(os.Stderr, "a2acli %s is the latest release (you have %s)\n", rel.TagName, cliVersion)
		}
		return nil
	}
	if !newer && !force {
		fmt.Fprintf(os.Stderr, "a2acli %s is up to date\n", cliVersion)
		return nil
	}

	fmt.Fprintf(os.Stderr, "downloading a2acli %s (%s)…\n", rel.TagName, selfupdate.AssetName(rel.TagName))
	bin, err := selfupdate.Download(ctx, rel)
	if err != nil {
		return fmt.Errorf("download update: %w", err)
	}
	path, err := selfupdate.Apply(bin)
	if err != nil {
		return fmt.Errorf("install update: %w", err)
	}
	fmt.Fprintf(os.Stderr, "updated a2acli %s → %s (%s)\n", cliVersion, rel.TagName, path)
	warnIfNotOnPath(path)
	return nil
}

func warnIfNotOnPath(path string) {
	dir := filepath.Dir(path)
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if p == dir {
			return
		}
	}
	fmt.Fprintf(os.Stderr, "\nnote: %s is not on your PATH\n", dir)
}

// newUpdateCheckHiddenCmd is the detached worker spawned by the passive
// check: it refreshes the cache and exits 0. Silent, never user-facing.
func newUpdateCheckHiddenCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "__update-check",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			rel, err := selfupdate.Latest(ctx)
			if err != nil {
				return nil // best-effort: a failed check just leaves the cache stale
			}
			_ = selfupdate.SaveState(selfupdate.State{CheckedAt: time.Now(), LatestVersion: rel.TagName})
			return nil
		},
	}
}

// maybeNotifyUpdate is the passive, best-effort update notice run before
// every command. It adds no network latency: it reads the cached state file
// (printing a one-line stderr notice when a newer release is known) and, if
// stale, spawns a detached child to refresh it for next time. No-op for dev
// builds, opted-out environments, the update commands themselves, and when
// stderr is not a terminal (keeps scripts and harness JSON pipelines quiet).
func maybeNotifyUpdate(cmd *cobra.Command) {
	if cliVersion == "dev" || os.Getenv(EnvNoUpdateCheck) != "" {
		return
	}
	switch cmd.Name() {
	case "update", "__update-check", "help", "completion", "version":
		return
	}
	if cmd.Parent() != nil && cmd.Parent().Name() == "completion" {
		return
	}
	if !term.IsTerminal(int(os.Stderr.Fd())) {
		return
	}

	state, err := selfupdate.LoadState()
	if err != nil {
		return
	}
	if state.LatestVersion != "" && selfupdate.Compare(state.LatestVersion, cliVersion) > 0 {
		fmt.Fprintf(os.Stderr, "a2acli: a newer version is available: %s (you have %s) — run `a2acli update`\n",
			state.LatestVersion, cliVersion)
	}
	if time.Since(state.CheckedAt) > checkInterval {
		spawnBackgroundCheck()
	}
}

// spawnBackgroundCheck launches `a2acli __update-check` fully detached and
// returns immediately. Launch failures are ignored.
func spawnBackgroundCheck() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return
	}
	defer func() { _ = devnull.Close() }()
	c := exec.Command(exe, "__update-check") //nolint:gosec // exe is this binary's own path
	c.Stdin, c.Stdout, c.Stderr = devnull, devnull, devnull
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := c.Start(); err == nil {
		_ = c.Process.Release()
	}
}
