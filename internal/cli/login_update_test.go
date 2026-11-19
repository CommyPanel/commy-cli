package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func runWith(t *testing.T, tweak func(*Environment), env map[string]string, args ...string) result {
	t.Helper()

	var out, errout bytes.Buffer

	vars := map[string]string{"COMMY_CONFIG": filepath.Join(t.TempDir(), "commy", "config.json"), "COMMY_ALLOW_HTTP_LOCALHOST": "1"}
	for k, v := range env {
		vars[k] = v
	}

	e := Environment{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errout, Getenv: func(k string) string { return vars[k] }, Version: "0.1.0"}
	tweak(&e)

	code := Run(args, e)

	return result{code, out.String(), errout.String()}
}

func TestLoginAsksForTheAddressAndAHiddenToken(t *testing.T) {
	f := newFake(t, map[string]func(int) (int, string){"GET /api/v1/token": always(200, tokenJSON)})
	config := filepath.Join(t.TempDir(), "commy", "config.json")

	r := runWith(t, func(e *Environment) {
		e.Stdin = strings.NewReader(f.URL + "\n")
		e.StdinIsTerminal = func() bool { return true }
		e.ReadSecret = func() (string, error) { return secret + "\n", nil }
	}, map[string]string{"COMMY_CONFIG": config}, "login")

	if r.code != 0 || !strings.Contains(r.out, "Signed in to") {
		t.Fatalf("%+v", r)
	}

	if !strings.Contains(r.errout, "typing is hidden") || strings.Contains(r.out+r.errout, "topsecret") {
		t.Errorf("the token must be asked for hidden and never printed: %+v", r)
	}

	if _, err := os.Stat(config); err != nil {
		t.Errorf("a good token is saved: %v", err)
	}
}

func TestLoginWithAWrongTokenSaysSoAndSavesNothing(t *testing.T) {
	f := newFake(t, map[string]func(int) (int, string){"GET /api/v1/token": always(401, `{"error":"Unauthenticated."}`)})
	config := filepath.Join(t.TempDir(), "config.json")

	r := runWith(t, func(e *Environment) {
		e.Stdin = strings.NewReader(f.URL + "\n")
		e.StdinIsTerminal = func() bool { return true }
		e.ReadSecret = func() (string, error) { return "wrong", nil }
	}, map[string]string{"COMMY_CONFIG": config}, "login")

	if r.code != ExitError || !strings.Contains(r.errout, "not valid") {
		t.Errorf("%+v", r)
	}

	if _, err := os.Stat(config); err == nil {
		t.Error("nothing may be saved for a wrong token")
	}
}

func TestLoginReadsAPipedTokenButNeverPromptsInAScript(t *testing.T) {
	f := newFake(t, map[string]func(int) (int, string){"GET /api/v1/token": always(200, tokenJSON)})

	r := runWith(t, func(e *Environment) {
		e.Stdin = strings.NewReader(secret + "\n")
		e.StdinIsTerminal = func() bool { return false }
	}, nil, "login", "--url", f.URL)

	if r.code != 0 || strings.Contains(r.errout, "typing is hidden") {
		t.Errorf("%+v", r)
	}

	r = runWith(t, func(e *Environment) { e.StdinIsTerminal = func() bool { return false } }, nil, "login")
	if r.code != ExitUsage || !strings.Contains(r.errout, "panel address") {
		t.Errorf("a script without an address must fail, not wait: %+v", r)
	}
}

// release serves the three things update talks to, over https.
func release(t *testing.T, latest string, binary []byte, sums string) *httptest.Server {
	t.Helper()

	name := fmt.Sprintf("commy_9.9.9_%s_%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		name += ".exe"
	}

	if sums == "" {
		h := sha256.Sum256(binary)
		sums = hex.EncodeToString(h[:]) + "  " + name + "\n"
	}

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/latest":
			http.Redirect(w, r, "/releases/tag/"+latest, http.StatusFound)
		case "/releases/download/v9.9.9/SHA256SUMS":
			_, _ = w.Write([]byte(sums))
		case "/releases/download/v9.9.9/" + name:
			_, _ = w.Write(binary)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	return srv
}

func updateRun(t *testing.T, srv *httptest.Server, exe string, version, stdin string, terminal bool, args ...string) result {
	t.Helper()

	return runWith(t, func(e *Environment) {
		e.Version = version
		e.HTTPClient = srv.Client()
		e.UpdateBase = srv.URL
		e.Executable = func() (string, error) { return exe, nil }
		e.Stdin = strings.NewReader(stdin)
		e.StdinIsTerminal = func() bool { return terminal }
	}, nil, args...)
}

func installedCopy(t *testing.T) string {
	t.Helper()

	exe := filepath.Join(t.TempDir(), "commy")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	return exe
}

func TestUpdateReplacesTheProgramAfterAYes(t *testing.T) {
	srv := release(t, "v9.9.9", []byte("new program"), "")
	exe := installedCopy(t)

	r := updateRun(t, srv, exe, "0.1.0", "y\n", true, "update")
	if r.code != 0 || !strings.Contains(r.out, "Updated to 9.9.9") {
		t.Fatalf("%+v", r)
	}

	if got, _ := os.ReadFile(exe); string(got) != "new program" {
		t.Errorf("the program was not replaced: %q", got)
	}

	if entries, _ := os.ReadDir(filepath.Dir(exe)); len(entries) != 1 {
		t.Errorf("no temporary files may be left: %v", entries)
	}
}

func TestUpdateDoesNothingOnNoOrWithoutAnyoneToAsk(t *testing.T) {
	srv := release(t, "v9.9.9", []byte("new program"), "")
	exe := installedCopy(t)

	r := updateRun(t, srv, exe, "0.1.0", "n\n", true, "update")
	if r.code != 0 || !strings.Contains(r.out, "Not updated") {
		t.Errorf("%+v", r)
	}

	r = updateRun(t, srv, exe, "0.1.0", "", false, "update")
	if r.code != ExitUsage || !strings.Contains(r.errout, "--yes") {
		t.Errorf("a script must pass --yes: %+v", r)
	}

	if got, _ := os.ReadFile(exe); string(got) != "old" {
		t.Errorf("the program must be untouched: %q", got)
	}

	r = updateRun(t, srv, exe, "0.1.0", "", false, "update", "--yes")
	if r.code != 0 || !strings.Contains(r.out, "Updated to 9.9.9") {
		t.Errorf("%+v", r)
	}
}

func TestUpdateCheckOnlyReports(t *testing.T) {
	srv := release(t, "v9.9.9", []byte("new program"), "")
	exe := installedCopy(t)

	r := updateRun(t, srv, exe, "0.1.0", "", false, "update", "--check")
	if r.code != 0 || !strings.Contains(r.out, "9.9.9") {
		t.Errorf("%+v", r)
	}

	if got, _ := os.ReadFile(exe); string(got) != "old" {
		t.Error("--check must not change anything")
	}

	r = updateRun(t, srv, exe, "9.9.9", "", false, "update", "--check")
	if !strings.Contains(r.out, "is the latest") {
		t.Errorf("%+v", r)
	}
}

func TestUpdateRefusesAWrongChecksum(t *testing.T) {
	srv := release(t, "v9.9.9", []byte("new program"), strings.Repeat("0", 64)+"  commy_9.9.9_"+runtime.GOOS+"_"+runtime.GOARCH+"\n")
	if runtime.GOOS == "windows" {
		t.Skip("the file name differs on Windows")
	}

	exe := installedCopy(t)

	r := updateRun(t, srv, exe, "0.1.0", "", false, "update", "--yes")
	if r.code != ExitError || !strings.Contains(r.errout, "checksum") {
		t.Errorf("%+v", r)
	}

	if got, _ := os.ReadFile(exe); string(got) != "old" {
		t.Error("a download that fails its checksum must never be installed")
	}
}

func TestUpdateIgnoresOddVersionsAndDevBuilds(t *testing.T) {
	srv := release(t, "v10.0.0-evil", []byte("x"), "")
	exe := installedCopy(t)

	r := updateRun(t, srv, exe, "0.1.0", "", false, "update", "--yes")
	if r.code != ExitError || !strings.Contains(r.errout, "release number") {
		t.Errorf("only plain vX.Y.Z tags count: %+v", r)
	}

	r = updateRun(t, srv, exe, "dev", "", false, "update", "--yes")
	if r.code != ExitError || !strings.Contains(r.errout, "development build") {
		t.Errorf("%+v", r)
	}
}

func TestVersionOrder(t *testing.T) {
	a, _ := parseVersion("v0.10.0")
	b, _ := parseVersion("v0.9.9")

	if !newer(a, b) || newer(b, a) || newer(a, a) {
		t.Error("versions must compare as numbers, not as text")
	}
}
