// Package dotty materializes the machine's dotfiles from a repository
// discovered through the exe.dev reflection endpoint. The image ships no
// coding agents or runtimes of its own; the discovered dotfiles installer
// owns them, so nothing is baked twice.
package dotty

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	DefaultReflectionURL = "https://reflection.int.exe.xyz"

	// The image matches integrations by type and comment: a github-type
	// integration whose comment names it as the dotfiles source.
	integrationType    = "github"
	integrationComment = "dotfiles"

	stateVersion = 1
)

// Statuses reported by Materialize.
const (
	StatusMaterialized        = "materialized"
	StatusAlreadyMaterialized = "already materialized"
	StatusSkipped             = "skipped"
)

type Options struct {
	ReflectionURL string
	HomeDir       string
	HTTPClient    *http.Client
	Stdout        io.Writer
	// Force re-runs convergence even when the marker says this repository
	// was already materialized.
	Force bool
}

type Result struct {
	Status  string
	RepoURL string
	Path    string
	Detail  string
}

type reflectionResponse struct {
	Integrations []reflectionIntegration `json:"integrations"`
}

type reflectionIntegration struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Comment string `json:"comment"`
	Help    string `json:"help"`
}

type materializedState struct {
	Version int    `json:"version"`
	RepoURL string `json:"repo_url"`
	Path    string `json:"path"`
	Commit  string `json:"commit"`
	Updated string `json:"updated"`
}

// Materialize discovers the dotfiles integration, clones the repository, and
// applies its unattended installer (script/setup). Absent or unreachable
// reflection is a normal state, not an error: the result is skipped and the
// caller exits zero. Clone and converge failures return errors so a retry
// (boot timer or manual rerun) can succeed later.
func Materialize(ctx context.Context, opts Options) (Result, error) {
	if opts.ReflectionURL == "" {
		opts.ReflectionURL = DefaultReflectionURL
	}
	if opts.HomeDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Result{}, fmt.Errorf("home dir: %w", err)
		}
		opts.HomeDir = home
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	if opts.Stdout == nil {
		opts.Stdout = io.Discard
	}

	repoURL, res, err := discover(ctx, opts)
	if err != nil || res.Status == StatusSkipped {
		return res, err
	}

	dest, err := checkoutPath(repoURL, opts.HomeDir)
	if err != nil {
		return Result{}, err
	}
	res.RepoURL = repoURL
	res.Path = dest

	marker := statePath(opts.HomeDir)
	if !opts.Force {
		st, err := readState(marker)
		if err == nil && st.RepoURL == repoURL && isGitDir(filepath.Join(dest, ".git")) {
			res.Status = StatusAlreadyMaterialized
			return res, nil
		}
	}

	if !isGitDir(filepath.Join(dest, ".git")) {
		if err := clone(ctx, repoURL, dest); err != nil {
			return res, fmt.Errorf("clone %s: %w", repoURL, err)
		}
	}

	commit, err := gitOutput(ctx, dest, "rev-parse", "HEAD")
	if err != nil {
		return res, fmt.Errorf("resolve commit: %w", err)
	}

	// Unattended variant: no stdin, so every prompt path in the installer
	// takes its automation branch; secret steps skip cleanly on a key-less
	// host by the installer's own contract.
	fmt.Fprintf(opts.Stdout, "materialize: converging %s\n", dest)
	setup := exec.CommandContext(ctx, "./script/setup")
	setup.Dir = dest
	setup.Stdin = nil
	setup.Stdout = opts.Stdout
	setup.Stderr = os.Stderr
	if err := setup.Run(); err != nil {
		return res, fmt.Errorf("converge %s: %w", dest, err)
	}

	st := materializedState{
		Version: stateVersion,
		RepoURL: repoURL,
		Path:    dest,
		Commit:  commit,
		Updated: time.Now().UTC().Format(time.RFC3339),
	}
	if err := writeState(marker, st); err != nil {
		return res, err
	}
	res.Status = StatusMaterialized
	return res, nil
}

// discover returns the clone URL of the dotfiles integration. A missing match
// or an unreachable endpoint yields StatusSkipped with a nil error.
func discover(ctx context.Context, opts Options) (string, Result, error) {
	integrationsURL, err := integrationsEndpoint(opts.ReflectionURL)
	if err != nil {
		return "", Result{}, err
	}
	var payload reflectionResponse
	if err := fetchJSON(ctx, opts.HTTPClient, integrationsURL, &payload); err != nil {
		return "", Result{Status: StatusSkipped, Detail: fmt.Sprintf("reflection unavailable: %v", err)}, nil
	}
	match := selectIntegration(payload.Integrations)
	if match == nil {
		return "", Result{Status: StatusSkipped, Detail: fmt.Sprintf(
			"no %s integration with comment %q", integrationType, integrationComment)}, nil
	}
	repoURL, err := parseCloneURL(match.Help)
	if err != nil {
		return "", Result{}, fmt.Errorf("integration %q help: %w", match.Name, err)
	}
	return repoURL, Result{}, nil
}

// selectIntegration picks the github integration whose comment names the
// dotfiles source; ties break alphabetically for determinism.
func selectIntegration(integrations []reflectionIntegration) *reflectionIntegration {
	candidates := make([]reflectionIntegration, 0, len(integrations))
	for _, in := range integrations {
		if in.Type == integrationType && strings.EqualFold(strings.TrimSpace(in.Comment), integrationComment) {
			candidates = append(candidates, in)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Name < candidates[j].Name })
	return &candidates[0]
}

var cloneRe = regexp.MustCompile(`git\s+clone\s+(\S+)`)

// parseCloneURL extracts the repository URL from an integration's help text
// ("git clone <url>"), falling back to a bare URL.
func parseCloneURL(help string) (string, error) {
	help = strings.TrimSpace(help)
	if m := cloneRe.FindStringSubmatch(help); m != nil {
		help = strings.Trim(m[1], "'\"")
	}
	u, err := url.Parse(help)
	if err != nil {
		return "", fmt.Errorf("parse url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("unsupported url %q", help)
	}
	if u.Host == "" || strings.Trim(u.Path, "/") == "" {
		return "", fmt.Errorf("url %q has no repository path", help)
	}
	return help, nil
}

// checkoutPath mirrors the dotty repos convention ${DOTTY_REPOS_ROOT:-$HOME/src}/<host>/<path>,
// so the clone lands where repos:add would place it.
func checkoutPath(repoURL, home string) (string, error) {
	u, err := url.Parse(repoURL)
	if err != nil {
		return "", fmt.Errorf("parse url: %w", err)
	}
	path := strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
	if u.Host == "" || path == "" {
		return "", fmt.Errorf("url %q has no repository path", repoURL)
	}
	return filepath.Join(home, "src", u.Host, filepath.FromSlash(path)), nil
}

func isGitDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

func clone(ctx context.Context, repoURL, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "git", "clone", "--quiet", repoURL, dest)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func integrationsEndpoint(reflectionURL string) (string, error) {
	u, err := url.Parse(reflectionURL)
	if err != nil {
		return "", fmt.Errorf("reflection URL: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("reflection URL must be absolute: %q", reflectionURL)
	}
	if strings.TrimRight(u.Path, "/") != "/integrations" {
		u.Path = strings.TrimRight(u.Path, "/") + "/integrations"
	}
	return u.String(), nil
}

func fetchJSON(ctx context.Context, client *http.Client, rawURL string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, 4*1024*1024))
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	return nil
}

func statePath(home string) string {
	return filepath.Join(home, ".config", "exe", "dotty-materialized.json")
}

func readState(path string) (materializedState, error) {
	var st materializedState
	data, err := os.ReadFile(path)
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return st, err
	}
	if st.Version != stateVersion {
		return st, errors.New("unsupported state version")
	}
	return st, nil
}

func writeState(path string, st materializedState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}
