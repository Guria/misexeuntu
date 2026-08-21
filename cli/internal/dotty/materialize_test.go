package dotty

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSelectIntegration(t *testing.T) {
	integrations := []reflectionIntegration{
		{Name: "llm", Type: "llm", Comment: "keyless gateway"},
		{Name: "cpamc", Type: "http-proxy", Comment: "gateway"},
		{Name: "dot", Type: "github", Comment: ""},
		{Name: "dotty", Type: "github", Comment: "dotfiles", Help: "git clone https://github.int.exe.xyz/Guria/dotty.git"},
		{Name: "notify", Type: "notify"},
	}
	got := selectIntegration(integrations)
	if got == nil || got.Name != "dotty" {
		t.Fatalf("selectIntegration() = %+v, want dotty", got)
	}
	if selectIntegration([]reflectionIntegration{{Name: "dot", Type: "github"}}) != nil {
		t.Fatal("github integration without dotfiles comment must not match")
	}
	if selectIntegration(nil) != nil {
		t.Fatal("empty integrations must not match")
	}
}

func TestSelectIntegrationTieBreaksAlphabetically(t *testing.T) {
	got := selectIntegration([]reflectionIntegration{
		{Name: "zeta", Type: "github", Comment: "Dotfiles "},
		{Name: "alpha", Type: "github", Comment: "dotfiles"},
	})
	if got == nil || got.Name != "alpha" {
		t.Fatalf("selectIntegration() = %+v, want alpha", got)
	}
}

func TestParseCloneURL(t *testing.T) {
	cases := []struct {
		help string
		want string
	}{
		{"git clone https://github.int.exe.xyz/Guria/dotty.git", "https://github.int.exe.xyz/Guria/dotty.git"},
		{"git clone 'https://github.int.exe.xyz/Guria/dotty.git'", "https://github.int.exe.xyz/Guria/dotty.git"},
		{"https://github.com/guria/dotty.git", "https://github.com/guria/dotty.git"},
	}
	for _, tc := range cases {
		got, err := parseCloneURL(tc.help)
		if err != nil || got != tc.want {
			t.Errorf("parseCloneURL(%q) = %q, %v; want %q", tc.help, got, err, tc.want)
		}
	}
	for _, help := range []string{"", "see the portal", "git clone ssh@host:dotty"} {
		if _, err := parseCloneURL(help); err == nil {
			t.Errorf("parseCloneURL(%q) = nil error, want error", help)
		}
	}
}

func TestCheckoutPath(t *testing.T) {
	got, err := checkoutPath("https://github.int.exe.xyz/Guria/dotty.git", "/home/exedev")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("/home/exedev", "src", "github.int.exe.xyz", "Guria", "dotty")
	if got != want {
		t.Fatalf("checkoutPath() = %q, want %q", got, want)
	}
}

func TestIntegrationsEndpoint(t *testing.T) {
	for _, in := range []string{
		"https://reflection.int.exe.xyz",
		"https://reflection.int.exe.xyz/",
		"https://reflection.int.exe.xyz/integrations",
	} {
		got, err := integrationsEndpoint(in)
		if err != nil || got != "https://reflection.int.exe.xyz/integrations" {
			t.Errorf("integrationsEndpoint(%q) = %q, %v", in, got, err)
		}
	}
}

func TestMaterializeSkipsWhenReflectionUnreachable(t *testing.T) {
	// A closed port: connection refused must degrade to a skip, not an error.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	res, err := Materialize(context.Background(), Options{
		ReflectionURL: server.URL,
		HomeDir:       t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Materialize() error = %v, want nil (graceful)", err)
	}
	if res.Status != StatusSkipped {
		t.Fatalf("status = %q, want %q", res.Status, StatusSkipped)
	}
}

func TestMaterializeSkipsWithoutMatchingIntegration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(reflectionResponse{Integrations: []reflectionIntegration{
			{Name: "llm", Type: "llm"},
			{Name: "dot", Type: "github"},
		}})
	}))
	defer server.Close()

	res, err := Materialize(context.Background(), Options{
		ReflectionURL: server.URL,
		HomeDir:       t.TempDir(),
	})
	if err != nil || res.Status != StatusSkipped {
		t.Fatalf("Materialize() = %+v, %v; want skipped", res, err)
	}
}

// The full flow against a fake reflection endpoint and a local git remote:
// clone into ~/src/<host>/<owner>/<repo>, run script/setup, write the marker,
// and short-circuit on the next run.
func TestMaterializeEndToEnd(t *testing.T) {
	ctx := context.Background()
	remote := newGitRemote(t)

	mux := http.NewServeMux()
	mux.HandleFunc("/integrations", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(reflectionResponse{Integrations: []reflectionIntegration{
			{Name: "dotty", Type: "github", Comment: "dotfiles",
				Help: "git clone " + remote},
		}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	home := t.TempDir()
	opts := Options{ReflectionURL: server.URL, HomeDir: home}

	res, err := Materialize(ctx, opts)
	if err != nil {
		t.Fatalf("first Materialize() error = %v", err)
	}
	if res.Status != StatusMaterialized {
		t.Fatalf("first status = %q, want %q", res.Status, StatusMaterialized)
	}
	if !strings.HasPrefix(res.Path, filepath.Join(home, "src")) {
		t.Fatalf("clone path %q is not under %s", res.Path, filepath.Join(home, "src"))
	}
	if _, err := os.Stat(filepath.Join(res.Path, "setup-ran")); err != nil {
		t.Fatalf("script/setup did not run in %s: %v", res.Path, err)
	}
	st, err := readState(statePath(home))
	if err != nil || st.RepoURL != remote || st.Commit == "" {
		t.Fatalf("marker = %+v, %v", st, err)
	}

	res, err = Materialize(ctx, opts)
	if err != nil || res.Status != StatusAlreadyMaterialized {
		t.Fatalf("second Materialize() = %+v, %v; want already materialized", res, err)
	}
}

func TestMaterializeForceReconverges(t *testing.T) {
	ctx := context.Background()
	remote := newGitRemote(t)

	mux := http.NewServeMux()
	mux.HandleFunc("/integrations", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(reflectionResponse{Integrations: []reflectionIntegration{
			{Name: "dotty", Type: "github", Comment: "dotfiles", Help: "git clone " + remote},
		}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	home := t.TempDir()
	if _, err := Materialize(ctx, Options{ReflectionURL: server.URL, HomeDir: home}); err != nil {
		t.Fatal(err)
	}
	res, err := Materialize(ctx, Options{ReflectionURL: server.URL, HomeDir: home, Force: true})
	if err != nil || res.Status != StatusMaterialized {
		t.Fatalf("forced Materialize() = %+v, %v; want materialized", res, err)
	}
}

// newGitRemote commits a script/setup that records its invocation and serves
// the bare repository over dumb HTTP, so the flow runs against a real
// http(s)-scheme URL like reflection hands out.
func newGitRemote(t *testing.T) string {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	work := t.TempDir()
	bare := filepath.Join(t.TempDir(), "dotty.git")

	setup := "#!/bin/sh\ntouch setup-ran\n"
	if err := os.MkdirAll(filepath.Join(work, "script"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "script", "setup"), []byte(setup), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(work, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	run("git", "-C", work, "init", "-q", "-b", "main")
	run("git", "-C", work, "add", ".")
	run("git", "-C", work, "commit", "-qm", "init")
	run("git", "-C", work, "clone", "--quiet", "--bare", work, bare)
	// Static file serving plus this index is all dumb HTTP clone needs.
	run("git", "-C", bare, "update-server-info")
	server := httptest.NewServer(http.FileServer(http.Dir(filepath.Dir(bare))))
	t.Cleanup(server.Close)
	return server.URL + "/dotty.git"
}
