package cli

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	defaultUpdateBase = "https://github.com/commypanel/commy-cli"
	maxBinaryBytes    = 100 << 20
	maxSumsBytes      = 64 << 10
)

var stableTag = regexp.MustCompile(`^v(\d{1,4})\.(\d{1,4})\.(\d{1,4})$`)

// update looks for a newer release and, after a yes, replaces this program with it. It is the only place
// commy calls out on its own, and only when a person runs it.
func (a *app) update(args []string) error {
	fs, _ := a.flags("update", "update [--check] [--yes]")

	var check, yes bool

	fs.BoolVar(&check, "check", false, "only say whether a newer version exists")
	fs.BoolVar(&yes, "yes", false, "install without asking (needed when nobody is at the keyboard)")

	pos, err := parse(fs, args)
	if err != nil {
		return err
	}

	if len(pos) != 0 {
		return usagef("update takes no arguments")
	}

	current, ok := parseVersion("v" + strings.TrimPrefix(a.env.Version, "v"))
	if !ok {
		return fmt.Errorf("this is a development build (%q), so there is nothing to compare; install a release instead", a.env.Version)
	}

	base := strings.TrimRight(firstNonEmpty(a.env.UpdateBase, defaultUpdateBase), "/")

	client := a.env.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}

	tag, err := a.latestTag(client, base)
	if err != nil {
		return err
	}

	latest, _ := parseVersion(tag)
	if !newer(latest, current) {
		fmt.Fprintf(a.out(), "commy %s is the latest version.\n", strings.TrimPrefix(a.env.Version, "v"))

		return nil
	}

	fmt.Fprintf(a.out(), "A newer version is available: %s (you have %s).\n", strings.TrimPrefix(tag, "v"), strings.TrimPrefix(a.env.Version, "v"))

	if check {
		fmt.Fprintln(a.out(), "Run `commy update` to install it.")

		return nil
	}

	exe, err := a.executable()
	if err != nil {
		return err
	}

	if !yes {
		if !a.env.interactive() {
			return usagef("nobody to ask: run `commy update --yes` to install without asking")
		}

		fmt.Fprintf(a.err(), "Download %s and replace %s? [y/N] ", strings.TrimPrefix(tag, "v"), exe)

		answer, _ := bufio.NewReader(io.LimitReader(a.env.Stdin, 256)).ReadString('\n')
		if answer = strings.ToLower(strings.TrimSpace(answer)); answer != "y" && answer != "yes" {
			fmt.Fprintln(a.out(), "Not updated.")

			return nil
		}
	}

	if err := a.install(client, base, tag, exe); err != nil {
		return err
	}

	fmt.Fprintf(a.out(), "Updated to %s. (%s)\n", strings.TrimPrefix(tag, "v"), exe)

	return nil
}

func (a *app) executable() (string, error) {
	find := a.env.Executable
	if find == nil {
		find = os.Executable
	}

	exe, err := find()
	if err != nil {
		return "", fmt.Errorf("cannot find where commy is installed: %w", err)
	}

	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}

	return exe, nil
}

// latestTag asks which release is the latest by reading where /releases/latest redirects to, without
// following it, so no page is parsed.
func (a *app) latestTag(client *http.Client, base string) (string, error) {
	probe := *client
	probe.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	resp, err := a.get(&probe, base+"/releases/latest", true)
	if err != nil {
		return "", fmt.Errorf("looking for the latest version: %w", err)
	}

	defer resp.Body.Close()

	location := resp.Header.Get("Location")
	if resp.StatusCode < 300 || resp.StatusCode > 399 || location == "" {
		return "", fmt.Errorf("looking for the latest version: unexpected answer (%d)", resp.StatusCode)
	}

	tag := location[strings.LastIndex(location, "/")+1:]
	if !stableTag.MatchString(tag) {
		return "", errors.New("looking for the latest version: the answer was not a release number")
	}

	return tag, nil
}

// get makes one GET. Only https is spoken; redirects (a download is sent to GitHub's file host) must stay
// on https, and with the real address on GitHub's own hosts. Nothing secret is ever sent.
func (a *app) get(client *http.Client, address string, noRedirect bool) (*http.Response, error) {
	req, err := http.NewRequestWithContext(a.ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}

	if req.URL.Scheme != "https" {
		return nil, errors.New("only https addresses are used")
	}

	req.Header.Set("User-Agent", "commy/"+a.env.Version)

	if !noRedirect {
		c := *client
		c.CheckRedirect = func(next *http.Request, via []*http.Request) error {
			if len(via) > 5 || next.URL.Scheme != "https" {
				return errors.New("refusing to follow that redirect")
			}

			if a.env.UpdateBase == "" && !githubHost(next.URL) {
				return fmt.Errorf("refusing to follow a redirect to %s", next.URL.Host)
			}

			return nil
		}
		client = &c
	}

	return client.Do(req)
}

func githubHost(u *url.URL) bool {
	h := strings.ToLower(u.Hostname())

	return h == "github.com" || strings.HasSuffix(h, ".github.com") || strings.HasSuffix(h, ".githubusercontent.com")
}

// install downloads the program for this system, checks it against the release's SHA256SUMS and swaps it in.
func (a *app) install(client *http.Client, base, tag, exe string) error {
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}

	name := fmt.Sprintf("commy_%s_%s_%s%s", strings.TrimPrefix(tag, "v"), runtime.GOOS, runtime.GOARCH, ext)
	release := base + "/releases/download/" + tag + "/"

	sums, err := a.fetch(client, release+"SHA256SUMS", maxSumsBytes)
	if err != nil {
		return fmt.Errorf("downloading the checksums: %w", err)
	}

	want := ""

	for _, line := range strings.Split(string(sums), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[1] == name {
			want = strings.ToLower(f[0])
		}
	}

	if want == "" {
		return fmt.Errorf("release %s has no build for %s/%s", tag, runtime.GOOS, runtime.GOARCH)
	}

	fmt.Fprintf(a.err(), "Downloading %s...\n", name)

	binary, err := a.fetch(client, release+name, maxBinaryBytes)
	if err != nil {
		return fmt.Errorf("downloading commy: %w", err)
	}

	sum := sha256.Sum256(binary)
	if hex.EncodeToString(sum[:]) != want {
		return errors.New("the checksum does not match: the download is damaged or has been tampered with. Nothing was changed")
	}

	return replaceExecutable(exe, binary)
}

func (a *app) fetch(client *http.Client, address string, limit int64) ([]byte, error) {
	resp, err := a.get(client, address, false)
	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the server answered %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}

	if int64(len(body)) > limit {
		return nil, errors.New("the file is larger than expected")
	}

	return body, nil
}

// replaceExecutable writes the new program next to the old one and swaps them, so a failure leaves the old
// one working. A running program cannot be overwritten on Windows, but it can be renamed.
func replaceExecutable(exe string, binary []byte) error {
	dir := filepath.Dir(exe)

	tmp, err := os.CreateTemp(dir, ".commy-update-*")
	if err != nil {
		return fmt.Errorf("cannot write to %s (%w). Run the update with the rights that folder needs, or reinstall with install.sh", dir, err)
	}

	tmpName := tmp.Name()

	cleanup := func() { _ = os.Remove(tmpName) }

	if _, err := tmp.Write(binary); err != nil {
		tmp.Close()
		cleanup()

		return err
	}

	if err := tmp.Close(); err != nil {
		cleanup()

		return err
	}

	if err := os.Chmod(tmpName, 0o755); err != nil {
		cleanup()

		return err
	}

	if runtime.GOOS == "windows" {
		old := exe + ".old"
		_ = os.Remove(old)

		if err := os.Rename(exe, old); err != nil {
			cleanup()

			return err
		}

		if err := os.Rename(tmpName, exe); err != nil {
			_ = os.Rename(old, exe)
			cleanup()

			return err
		}

		_ = os.Remove(old)

		return nil
	}

	if err := os.Rename(tmpName, exe); err != nil {
		cleanup()

		return err
	}

	return nil
}

func parseVersion(tag string) ([3]int, bool) {
	m := stableTag.FindStringSubmatch(tag)
	if m == nil {
		return [3]int{}, false
	}

	var v [3]int

	for i := range v {
		v[i], _ = strconv.Atoi(m[i+1])
	}

	return v, true
}

func newer(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}

	return false
}
