package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

const secret = "9|cpt_topsecrettopsecrettopsecrettopsecret"

type call struct {
	Method, Path, Query, Body, Auth string
}

// fakePanel answers by "METHOD /path" with a status and a body, and records what it was asked.
type fakePanel struct {
	*httptest.Server
	mu     sync.Mutex
	routes map[string]func(n int) (int, string)
	calls  []call
	counts map[string]int
}

func newFake(t *testing.T, routes map[string]func(n int) (int, string)) *fakePanel {
	t.Helper()

	f := &fakePanel{routes: routes, counts: map[string]int{}}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		key := r.Method + " " + r.URL.Path

		f.mu.Lock()
		f.calls = append(f.calls, call{r.Method, r.URL.Path, r.URL.RawQuery, string(body), r.Header.Get("Authorization")})
		f.counts[key]++
		n := f.counts[key]
		f.mu.Unlock()

		handler, ok := f.routes[key]
		if !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":"Not Found"}`))

			return
		}

		status, reply := handler(n)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(f.Close)

	return f
}

func always(status int, body string) func(int) (int, string) {
	return func(int) (int, string) { return status, body }
}

func (f *fakePanel) made() []call {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]call(nil), f.calls...)
}

type result struct {
	code        int
	out, errout string
}

type setup struct {
	env      map[string]string
	stdin    string
	terminal bool
}

// run executes commy with its own config file in a temporary folder.
func run(t *testing.T, s setup, args ...string) result {
	t.Helper()

	var out, errout bytes.Buffer

	env := map[string]string{"COMMY_CONFIG": filepath.Join(t.TempDir(), "commy", "config.json")}
	for k, v := range s.env {
		env[k] = v
	}

	code := Run(args, Environment{
		Stdin:           strings.NewReader(s.stdin),
		Stdout:          &out,
		Stderr:          &errout,
		Getenv:          func(k string) string { return env[k] },
		Version:         "test",
		StdinIsTerminal: func() bool { return s.terminal },
	})

	return result{code, out.String(), errout.String()}
}

func withToken(f *fakePanel, extra map[string]string) setup {
	env := map[string]string{"COMMY_URL": f.URL, "COMMY_TOKEN": secret, "COMMY_ALLOW_HTTP_LOCALHOST": "1"}
	for k, v := range extra {
		env[k] = v
	}

	return setup{env: env}
}

const tokenJSON = `{"data":{"name":"ci","abilities":["apps:read","apps:deploy"],"apps":["shop"],"expires_at":null,"last_used_at":null,"owner":"Sam"}}`

func depJSON(number int, status, stage string, finished, succeeded bool, errText string) string {
	e := "null"
	if errText != "" {
		e = fmt.Sprintf("%q", errText)
	}

	return fmt.Sprintf(`{"number":%d,"status":%q,"stage":%q,"finished":%v,"succeeded":%v,"commit":"abc123","commit_message":"Fix","strategy":"rolling","is_rollback":false,"error":%s,"created_at":null,"finished_at":null,"url":"u"}`, number, status, stage, finished, succeeded, e)
}

func TestLoginVerifiesTheTokenAndSavesItPrivately(t *testing.T) {
	f := newFake(t, map[string]func(int) (int, string){"GET /api/v1/token": always(200, tokenJSON)})
	config := filepath.Join(t.TempDir(), "commy", "config.json")

	r := run(t, setup{env: map[string]string{"COMMY_TOKEN": secret, "COMMY_CONFIG": config}}, "login", "--url", f.URL, "--allow-http-localhost")

	if r.code != 0 || !strings.Contains(r.out, `Signed in to`) || !strings.Contains(r.out, "ci") || !strings.Contains(r.out, "shop") {
		t.Fatalf("%+v", r)
	}

	if strings.Contains(r.out+r.errout, "topsecret") {
		t.Error("the token must not be printed")
	}

	info, err := os.Stat(config)
	if err != nil {
		t.Fatal(err)
	}

	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("config mode = %o, want 600", info.Mode().Perm())
	}

	raw, _ := os.ReadFile(config)

	var saved config2
	if json.Unmarshal(raw, &saved) != nil || saved.Token != secret || saved.URL != f.URL {
		t.Errorf("saved = %s", raw)
	}

	// Later commands use the saved login with no environment at all.
	later := run(t, setup{env: map[string]string{"COMMY_CONFIG": config, "COMMY_ALLOW_HTTP_LOCALHOST": "1"}}, "whoami")
	if later.code != 0 || !strings.Contains(later.out, "Sam") {
		t.Errorf("%+v", later)
	}

	if f.made()[len(f.made())-1].Auth != "Bearer "+secret {
		t.Error("the saved token should be sent as a bearer token")
	}
}

type config2 struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

func TestLoginNeverTakesTheTokenOnTheCommandLine(t *testing.T) {
	f := newFake(t, nil)

	r := run(t, setup{}, "login", "--url", f.URL, "--allow-http-localhost")
	if r.code != ExitUsage || !strings.Contains(r.errout, "COMMY_TOKEN") {
		t.Errorf("%+v", r)
	}

	r = run(t, setup{}, "login", "--url", f.URL, "--token", secret)
	if r.code != ExitUsage || strings.Contains(r.out+r.errout, "topsecret") {
		t.Errorf("an unknown --token flag must be a usage error that does not echo the value: %+v", r)
	}

	if len(f.made()) != 0 {
		t.Error("nothing should have reached the panel")
	}
}

func TestLoginDoesNotSaveATokenThePanelRefuses(t *testing.T) {
	f := newFake(t, map[string]func(int) (int, string){"GET /api/v1/token": always(401, `{"error":"Unauthenticated."}`)})
	config := filepath.Join(t.TempDir(), "config.json")

	r := run(t, setup{env: map[string]string{"COMMY_TOKEN": secret, "COMMY_CONFIG": config}}, "login", "--url", f.URL, "--allow-http-localhost")

	if r.code != ExitError || !strings.Contains(r.errout, "not valid") || strings.Contains(r.out+r.errout, "topsecret") {
		t.Errorf("%+v", r)
	}

	if _, err := os.Stat(config); err == nil {
		t.Error("a token the panel refused must not be saved")
	}
}

func TestAHttpPanelIsRefusedUnlessItIsThisMachineAndAskedFor(t *testing.T) {
	f := newFake(t, map[string]func(int) (int, string){"GET /api/v1/token": always(200, tokenJSON)})

	r := run(t, setup{env: map[string]string{"COMMY_URL": f.URL, "COMMY_TOKEN": secret}}, "whoami")
	if r.code != ExitError || !strings.Contains(r.errout, "https") || len(f.made()) != 0 {
		t.Errorf("%+v", r)
	}

	r = run(t, setup{env: map[string]string{"COMMY_URL": "http://panel.example.com", "COMMY_TOKEN": secret}}, "whoami", "--allow-http-localhost")
	if r.code != ExitError || !strings.Contains(r.errout, "https") {
		t.Errorf("only loopback may use http, even when asked: %+v", r)
	}
}

func TestAConfigFileOthersCanReadIsRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no unix permissions")
	}

	f := newFake(t, nil)
	config := filepath.Join(t.TempDir(), "config.json")
	_ = os.WriteFile(config, []byte(`{"url":"`+f.URL+`","token":"`+secret+`"}`), 0o644)

	r := run(t, setup{env: map[string]string{"COMMY_CONFIG": config, "COMMY_ALLOW_HTTP_LOCALHOST": "1"}}, "apps")

	if r.code != ExitError || !strings.Contains(r.errout, "chmod 600") || len(f.made()) != 0 || strings.Contains(r.errout, "topsecret") {
		t.Errorf("%+v", r)
	}
}

func TestASavedTokenIsNeverSentToAnotherAddress(t *testing.T) {
	home := newFake(t, map[string]func(int) (int, string){"GET /api/v1/apps": always(200, `{"data":[]}`)})
	other := newFake(t, map[string]func(int) (int, string){"GET /api/v1/apps": always(200, `{"data":[]}`)})
	config := filepath.Join(t.TempDir(), "config.json")
	_ = os.WriteFile(config, []byte(`{"url":"`+home.URL+`","token":"`+secret+`"}`), 0o600)
	env := map[string]string{"COMMY_CONFIG": config, "COMMY_ALLOW_HTTP_LOCALHOST": "1"}

	r := run(t, setup{env: env}, "apps", "--url", other.URL)

	if r.code != ExitError || !strings.Contains(r.errout, "not sent there") || len(other.made()) != 0 {
		t.Errorf("%+v / other saw %d", r, len(other.made()))
	}

	// The same address (even with a trailing slash) is fine.
	if r := run(t, setup{env: env}, "apps", "--url", home.URL+"/"); r.code != 0 {
		t.Errorf("%+v", r)
	}

	// Another panel needs its own token, given on purpose.
	env["COMMY_TOKEN"] = "5|cpt_other"
	if r := run(t, setup{env: env}, "apps", "--url", other.URL); r.code != 0 || other.made()[0].Auth != "Bearer 5|cpt_other" {
		t.Errorf("%+v", r)
	}
}

func TestAppsListShowAndJSON(t *testing.T) {
	app := `{"slug":"shop","name":"Shop","status":"running","stack":"go","deploy_strategy":"stop_first","domains":["shop.example.com"],"live_deployment":3,"latest_deployment":` + depJSON(3, "active", "done", true, true, "") + `,"created_at":"2026-11-16T10:00:00+00:00"}`
	f := newFake(t, map[string]func(int) (int, string){
		"GET /api/v1/apps":      always(200, `{"data":[`+app+`]}`),
		"GET /api/v1/apps/shop": always(200, `{"data":`+app+`}`),
	})

	list := run(t, withToken(f, nil), "apps", "list")
	if list.code != 0 || !strings.Contains(list.out, "shop") || !strings.Contains(list.out, "#3") || !strings.Contains(list.out, "shop.example.com") {
		t.Errorf("%+v", list)
	}

	show := run(t, withToken(f, nil), "apps", "show", "shop")
	if show.code != 0 || !strings.Contains(show.out, "stop the old version first") || !strings.Contains(show.out, "abc123") {
		t.Errorf("%+v", show)
	}

	asJSON := run(t, withToken(f, nil), "apps", "--json")

	var parsed []map[string]any
	if asJSON.code != 0 || json.Unmarshal([]byte(asJSON.out), &parsed) != nil || len(parsed) != 1 || parsed[0]["slug"] != "shop" {
		t.Errorf("%+v", asJSON)
	}

	missing := run(t, withToken(f, nil), "apps", "show", "nope")
	if missing.code != ExitError || !strings.Contains(missing.errout, "not found") {
		t.Errorf("%+v", missing)
	}
}

func TestDeployWaitsAndReportsTheResult(t *testing.T) {
	f := newFake(t, map[string]func(int) (int, string){
		"POST /api/v1/apps/shop/deployments": always(202, `{"data":`+depJSON(5, "queued", "", false, false, "")+`}`),
		"GET /api/v1/apps/shop/deployments/5": func(n int) (int, string) {
			if n < 2 {
				return 200, `{"data":` + depJSON(5, "building", "build", false, false, "") + `}`
			}

			return 200, `{"data":` + depJSON(5, "active", "done", true, true, "") + `}`
		},
	})

	r := run(t, withToken(f, nil), "deploy", "shop", "--poll", "1s")

	if r.code != 0 || !strings.Contains(r.out, "active") || !strings.Contains(r.errout, "building (build)") {
		t.Fatalf("%+v", r)
	}
}

func TestDeployExitCodesTellFailureFromSuccess(t *testing.T) {
	failing := newFake(t, map[string]func(int) (int, string){
		"POST /api/v1/apps/shop/deployments":  always(202, `{"data":`+depJSON(6, "queued", "", false, false, "")+`}`),
		"GET /api/v1/apps/shop/deployments/6": always(200, `{"data":`+depJSON(6, "failed", "health", true, false, "The app did not answer.")+`}`),
	})

	r := run(t, withToken(failing, nil), "deploy", "shop")
	if r.code != ExitDeployFailed || !strings.Contains(r.errout, "failed") || !strings.Contains(r.errout, "The app did not answer.") {
		t.Errorf("failed: %+v", r)
	}

	rolled := newFake(t, map[string]func(int) (int, string){
		"POST /api/v1/apps/shop/deployments":  always(202, `{"data":`+depJSON(7, "queued", "", false, false, "")+`}`),
		"GET /api/v1/apps/shop/deployments/7": always(200, `{"data":`+depJSON(7, "rolled_back", "done", true, false, "")+`}`),
	})

	if r := run(t, withToken(rolled, nil), "deploy", "shop"); r.code != ExitDeployFailed || !strings.Contains(r.errout, "rolled_back") {
		t.Errorf("rolled back: %+v", r)
	}
}

func TestDeployWhileOneRunsNamesIt(t *testing.T) {
	f := newFake(t, map[string]func(int) (int, string){
		"POST /api/v1/apps/shop/deployments": always(409, `{"error":"A deployment is already in progress.","deployment":`+depJSON(9, "building", "build", false, false, "")+`}`),
	})

	r := run(t, withToken(f, nil), "deploy", "shop")

	if r.code != ExitError || !strings.Contains(r.errout, "already in progress") || !strings.Contains(r.errout, "commy deployment shop 9") {
		t.Errorf("%+v", r)
	}
}

func TestDeployNoWaitAndTimeout(t *testing.T) {
	f := newFake(t, map[string]func(int) (int, string){
		"POST /api/v1/apps/shop/deployments":  always(202, `{"data":`+depJSON(8, "queued", "", false, false, "")+`}`),
		"GET /api/v1/apps/shop/deployments/8": always(200, `{"data":`+depJSON(8, "building", "build", false, false, "")+`}`),
	})

	quick := run(t, withToken(f, nil), "deploy", "shop", "--no-wait")
	if quick.code != 0 || !strings.Contains(quick.out, "Queued shop #8") || len(f.made()) != 1 {
		t.Errorf("%+v", quick)
	}

	slow := run(t, withToken(f, nil), "deploy", "shop", "--timeout", "1s")
	if slow.code != ExitWaitTimedOut || !strings.Contains(slow.errout, "commy deployment shop 8") {
		t.Errorf("%+v", slow)
	}
}

func TestApplyUsesTheApplyCall(t *testing.T) {
	f := newFake(t, map[string]func(int) (int, string){
		"POST /api/v1/apps/shop/apply": always(202, `{"data":`+depJSON(2, "queued", "", false, false, "")+`}`),
	})

	r := run(t, withToken(f, nil), "apply", "shop", "--no-wait")

	if r.code != 0 || f.made()[0].Path != "/api/v1/apps/shop/apply" {
		t.Errorf("%+v %v", r, f.made())
	}
}

func TestStartStopRestart(t *testing.T) {
	app := `{"data":{"slug":"shop","name":"Shop","status":"stopped","stack":null,"deploy_strategy":"rolling","domains":[],"live_deployment":1,"latest_deployment":null,"created_at":"2026-11-16T10:00:00+00:00"}}`
	f := newFake(t, map[string]func(int) (int, string){
		"POST /api/v1/apps/shop/stop":    always(200, app),
		"POST /api/v1/apps/shop/start":   always(200, app),
		"POST /api/v1/apps/shop/restart": always(200, app),
	})

	for _, action := range []string{"stop", "start", "restart"} {
		if r := run(t, withToken(f, nil), action, "shop"); r.code != 0 || !strings.Contains(r.out, "shop: stopped") {
			t.Errorf("%s: %+v", action, r)
		}
	}

	if len(f.made()) != 3 || f.made()[0].Path != "/api/v1/apps/shop/stop" {
		t.Errorf("%v", f.made())
	}
}

func TestLogsNeverSendControlCharactersToTheTerminal(t *testing.T) {
	// An app can print anything: a window-title change, a clipboard write (OSC 52), text hidden with a
	// carriage return or a bidirectional override.
	hostile := "ok line\\n\\u001b]0;pwned\\u0007\\u001b]52;c;ZXZpbA==\\u0007visible\\rhidden\\u202egnirts\\u009b31m\\tkept\\n"
	f := newFake(t, map[string]func(int) (int, string){
		"GET /api/v1/apps/shop/logs": always(200, `{"data":{"live":true,"lines":200,"logs":"`+hostile+`"}}`),
	})

	r := run(t, withToken(f, nil), "logs", "shop", "--tail", "50")

	if r.code != 0 {
		t.Fatalf("%+v", r)
	}

	for _, bad := range []string{"\x1b", "\x07", "\r", "‮", "\u009b"} {
		if strings.Contains(r.out, bad) {
			t.Errorf("the output still has %q: %q", bad, r.out)
		}
	}

	if !strings.Contains(r.out, "ok line") || !strings.Contains(r.out, "\tkept") || f.made()[0].Query != "tail=50" {
		t.Errorf("%q / %v", r.out, f.made())
	}
}

func TestLogsOfAnAppThatIsNotRunning(t *testing.T) {
	f := newFake(t, map[string]func(int) (int, string){"GET /api/v1/apps/shop/logs": always(200, `{"data":{"live":false,"lines":200,"logs":""}}`)})

	r := run(t, withToken(f, nil), "logs", "shop")

	if r.code != 0 || r.out != "" || !strings.Contains(r.errout, "no running container") {
		t.Errorf("%+v", r)
	}
}

func TestEnvListShowsNamesOnly(t *testing.T) {
	f := newFake(t, map[string]func(int) (int, string){"GET /api/v1/apps/shop/env": always(200, `{"data":{"keys":["A","B"],"pending_changes":true}}`)})

	r := run(t, withToken(f, nil), "env", "list", "shop")

	if r.code != 0 || r.out != "A\nB\n" || !strings.Contains(r.errout, "commy apply shop") {
		t.Errorf("%+v", r)
	}
}

func TestEnvSetReadsTheValueFromStdinAndNeverFromTheCommandLine(t *testing.T) {
	f := newFake(t, map[string]func(int) (int, string){
		"PUT /api/v1/apps/shop/env/STRIPE_KEY": always(201, `{"data":{"key":"STRIPE_KEY","pending_changes":true}}`),
	})

	s := withToken(f, nil)
	s.stdin = "sk_live_the-value\n"

	r := run(t, s, "env", "set", "shop", "STRIPE_KEY")

	if r.code != 0 || !strings.Contains(r.out, "STRIPE_KEY created") || !strings.Contains(r.out, "commy apply shop") {
		t.Fatalf("%+v", r)
	}

	if got := f.made()[0].Body; got != `{"value":"sk_live_the-value"}` {
		t.Errorf("body = %s", got)
	}

	if strings.Contains(r.out+r.errout, "sk_live") {
		t.Error("the value must never be printed")
	}

	// A third argument (a value on the command line) is a usage error, and the value is not echoed.
	s.stdin = ""
	if r := run(t, s, "env", "set", "shop", "STRIPE_KEY", "sk_live_typed"); r.code != ExitUsage || strings.Contains(r.out+r.errout, "sk_live_typed") {
		t.Errorf("%+v", r)
	}
}

func TestEnvSetRefusesATerminalAndMakesNoRequest(t *testing.T) {
	f := newFake(t, nil)
	s := withToken(f, nil)
	s.terminal = true

	r := run(t, s, "env", "set", "shop", "KEY")

	if r.code != ExitUsage || !strings.Contains(r.errout, "pipe the value in") || len(f.made()) != 0 {
		t.Errorf("%+v", r)
	}
}

func TestEnvSetFromFileAndLimits(t *testing.T) {
	f := newFake(t, map[string]func(int) (int, string){
		"PUT /api/v1/apps/shop/env/CERT": always(200, `{"data":{"key":"CERT","pending_changes":false}}`),
	})
	file := filepath.Join(t.TempDir(), "cert.pem")
	_ = os.WriteFile(file, []byte("line1\nline2\n"), 0o600)

	r := run(t, withToken(f, nil), "env", "set", "shop", "CERT", "--from-file", file)
	if r.code != 0 || !strings.Contains(r.out, "CERT updated") || f.made()[0].Body != `{"value":"line1\nline2"}` {
		t.Errorf("%+v body %s", r, f.made()[0].Body)
	}

	big := filepath.Join(t.TempDir(), "big")
	_ = os.WriteFile(big, bytes.Repeat([]byte("x"), maxEnvValue+10), 0o600)

	if r := run(t, withToken(f, nil), "env", "set", "shop", "CERT", "--from-file", big); r.code != ExitError || !strings.Contains(r.errout, "longer than") || len(f.made()) != 1 {
		t.Errorf("%+v", r)
	}
}

func TestEnvUnset(t *testing.T) {
	f := newFake(t, map[string]func(int) (int, string){
		"DELETE /api/v1/apps/shop/env/OLD": always(200, `{"data":{"key":"OLD","pending_changes":true}}`),
	})

	r := run(t, withToken(f, nil), "env", "unset", "shop", "OLD")

	if r.code != 0 || !strings.Contains(r.out, "OLD removed") {
		t.Errorf("%+v", r)
	}
}

func TestUsageMistakes(t *testing.T) {
	f := newFake(t, nil)

	for _, args := range [][]string{{}, {"nonsense"}, {"deploy"}, {"deploy", "a", "b"}, {"apps", "frobnicate"}, {"env"}, {"env", "set", "shop"}, {"deployment", "shop"}, {"deployment", "shop", "x"}, {"logs"}, {"deploy", "shop", "--poll", "10ms"}} {
		if r := run(t, withToken(f, nil), args...); r.code != ExitUsage {
			t.Errorf("%v: exit %d (%q)", args, r.code, r.errout)
		}
	}

	if len(f.made()) != 0 {
		t.Error("usage mistakes must not reach the panel")
	}

	if r := run(t, setup{}, "help"); r.code != 0 || !strings.Contains(r.errout, "commy controls") {
		t.Errorf("%+v", r)
	}

	if r := run(t, setup{}, "version"); r.code != 0 || r.out != "commy test\n" {
		t.Errorf("%+v", r)
	}
}

func TestErrorsExplainWhatToDo(t *testing.T) {
	cases := map[int]string{401: "not valid", 403: "not allowed", 404: "not found", 429: "too many calls"}

	for status, want := range cases {
		f := newFake(t, map[string]func(int) (int, string){"GET /api/v1/apps": always(status, `{"error":"x"}`)})

		r := run(t, withToken(f, nil), "apps")

		if r.code != ExitError || !strings.Contains(r.errout, want) {
			t.Errorf("%d: %+v", status, r)
		}

		if strings.Contains(r.out+r.errout, "topsecret") {
			t.Errorf("%d: the token is in the output", status)
		}
	}
}

func TestSafeText(t *testing.T) {
	cases := map[string]string{
		"plain text\twith tab\nand line": "plain text\twith tab\nand line",
		"a\x1b[31mred":                   "a�[31mred",
		"bell\x07":                       "bell�",
		"over\rwrite":                    "over\nwrite",
		"c1\u009bcode":                   "c1�code",
		"bidi‮txt":                       "bidi�txt",
		"del\x7f":                        "del�",
		"unicode ok: äöü 日本語 🙂":          "unicode ok: äöü 日本語 🙂",
	}

	for in, want := range cases {
		if got := safe(in); got != want {
			t.Errorf("safe(%q) = %q, want %q", in, got, want)
		}
	}
}
