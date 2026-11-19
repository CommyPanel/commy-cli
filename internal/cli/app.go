// Package cli is the commy command: it parses the command line, finds the panel address and token, and calls
// the commypanel-go library.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"

	commypanel "github.com/commypanel/commypanel-go"
	"golang.org/x/term"
)

// Exit codes. Scripts can tell a deployment that failed from a command that could not run.
const (
	ExitOK           = 0
	ExitError        = 1
	ExitUsage        = 2
	ExitDeployFailed = 3
	ExitWaitTimedOut = 4
	envURL           = "COMMY_URL"
	envToken         = "COMMY_TOKEN"
	envAllowLoopback = "COMMY_ALLOW_HTTP_LOCALHOST"
)

// Environment is everything the command touches outside itself, so tests can replace it.
type Environment struct {
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
	Getenv  func(string) string
	Version string

	// StdinIsTerminal says whether input comes from a person typing. Nil means "look at os.Stdin".
	StdinIsTerminal func() bool

	// ReadSecret reads one line typed without being shown. Nil means "ask the terminal".
	ReadSecret func() (string, error)

	// The rest is for the update command. Nil or empty means the real thing.
	HTTPClient *http.Client
	UpdateBase string
	Executable func() (string, error)
}

func (e Environment) readSecret() (string, error) {
	if e.ReadSecret != nil {
		return e.ReadSecret()
	}

	raw, err := term.ReadPassword(int(os.Stdin.Fd()))

	return string(raw), err
}

func (e Environment) interactive() bool {
	if e.StdinIsTerminal != nil {
		return e.StdinIsTerminal()
	}

	info, err := os.Stdin.Stat()

	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// usageError is a mistake in how the command was typed.
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func usagef(format string, args ...any) error { return usageError{fmt.Sprintf(format, args...)} }

// exitError makes a command end with a specific code after it already said what it wanted to say.
type exitError struct {
	code int
	msg  string
}

func (e exitError) Error() string { return e.msg }

// Run executes one command line and returns the process exit code.
func Run(args []string, env Environment) int {
	if env.Getenv == nil {
		env.Getenv = func(string) string { return "" }
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	a := &app{env: env, ctx: ctx}

	err := a.dispatch(args)
	if err == nil {
		return ExitOK
	}

	var usage usageError
	var exit exitError

	switch {
	case errors.As(err, &exit):
		if exit.msg != "" {
			fmt.Fprintln(env.Stderr, "commy:", safe(exit.msg))
		}

		return exit.code
	case errors.Is(err, flag.ErrHelp):
		return ExitOK
	case errors.As(err, &usage):
		fmt.Fprintf(env.Stderr, "commy: %s\nRun `commy help` for the commands.\n", safe(usage.msg))

		return ExitUsage
	default:
		fmt.Fprintln(env.Stderr, "commy:", safe(a.describe(err)))

		return ExitError
	}
}

type app struct {
	env Environment
	ctx context.Context
}

func (a *app) out() io.Writer { return a.env.Stdout }
func (a *app) err() io.Writer { return a.env.Stderr }

func (a *app) dispatch(args []string) error {
	if len(args) == 0 {
		a.help()

		return usagef("no command given")
	}

	command, rest := args[0], args[1:]

	switch command {
	case "help", "-h", "--help":
		a.help()

		return nil
	case "version", "--version":
		fmt.Fprintln(a.out(), "commy", a.env.Version)

		return nil
	case "login":
		return a.login(rest)
	case "logout":
		return a.logout(rest)
	case "update":
		return a.update(rest)
	case "whoami":
		return a.whoami(rest)
	case "apps":
		return a.apps(rest)
	case "deploy":
		return a.deploy(rest, false)
	case "apply":
		return a.deploy(rest, true)
	case "deployments":
		return a.deployments(rest)
	case "deployment":
		return a.deployment(rest)
	case "start", "stop", "restart":
		return a.control(command, rest)
	case "logs":
		return a.logs(rest)
	case "env":
		return a.envCommand(rest)
	default:
		return usagef("unknown command %q", command)
	}
}

// common holds the flags every command that talks to a panel accepts.
type common struct {
	url           string
	caFile        string
	allowLoopback bool
	json          bool
}

func (c *common) register(fs *flag.FlagSet, withJSON bool) {
	fs.StringVar(&c.url, "url", "", "the panel address (default: $COMMY_URL, or what `commy login` saved)")
	fs.StringVar(&c.caFile, "ca-file", "", "a PEM file with the certificate authority of a panel that uses a private one")
	fs.BoolVar(&c.allowLoopback, "allow-http-localhost", false, "allow plain http, for a panel on this machine only (localhost, 127.0.0.1, ::1)")

	if withJSON {
		fs.BoolVar(&c.json, "json", false, "print the answer as JSON")
	}
}

// flags makes a flag set that prints its own usage to stderr.
func (a *app) flags(name, usage string) (*flag.FlagSet, *common) {
	fs := flag.NewFlagSet("commy "+name, flag.ContinueOnError)
	fs.SetOutput(a.err())
	fs.Usage = func() {
		fmt.Fprintf(a.err(), "Usage: commy %s\n\n", usage)
		fs.PrintDefaults()
	}

	return fs, &common{}
}

// parse reads flags that may come before or after the positional arguments (`commy deploy shop --no-wait`).
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string

	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, flag.ErrHelp
			}

			return nil, usagef("%s", err)
		}

		rest := fs.Args()
		if len(rest) == 0 {
			return positional, nil
		}

		positional = append(positional, rest[0])
		args = rest[1:]
	}
}

// settings is what a command needs to reach the panel.
type settings struct {
	url    string
	token  string
	caFile string
	source string // where the token came from, for messages
}

// resolve finds the panel address and token: flags, then the environment, then the saved config. A token
// saved by `commy login` belongs to the address it was saved with and is never sent anywhere else.
func (a *app) resolve(c *common) (settings, error) {
	path, err := configPath(a.env.Getenv)
	if err != nil {
		return settings{}, err
	}

	saved, err := loadConfig(path)
	if err != nil {
		return settings{}, err
	}

	s := settings{url: firstNonEmpty(c.url, a.env.Getenv(envURL), saved.URL), caFile: firstNonEmpty(c.caFile, saved.CAFile)}

	if s.url == "" {
		return s, usagef("no panel address: run `commy login --url https://panel.example.com`, or set %s", envURL)
	}

	if token := a.env.Getenv(envToken); token != "" {
		s.token, s.source = token, envToken

		return s, nil
	}

	if saved.Token == "" {
		return s, errors.New("not signed in: run `commy login`, or set " + envToken)
	}

	if !sameAddress(s.url, saved.URL) {
		return s, fmt.Errorf("the saved token belongs to %s, not to %s, and is not sent there; set %s to use another panel", saved.URL, s.url, envToken)
	}

	s.token, s.source = saved.Token, "the saved login"

	return s, nil
}

func sameAddress(a, b string) bool {
	return strings.TrimRight(strings.ToLower(a), "/") == strings.TrimRight(strings.ToLower(b), "/")
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}

	return ""
}

// client builds the API client for a command.
func (a *app) client(c *common) (*commypanel.Client, error) {
	s, err := a.resolve(c)
	if err != nil {
		return nil, err
	}

	return a.newClient(s, c)
}

func (a *app) newClient(s settings, c *common) (*commypanel.Client, error) {
	opts := []commypanel.Option{commypanel.WithUserAgent("commy/" + a.env.Version)}

	if c.allowLoopback || a.env.Getenv(envAllowLoopback) == "1" {
		opts = append(opts, commypanel.WithAllowHTTPLoopback())
	}

	if s.caFile != "" {
		pem, err := os.ReadFile(s.caFile)
		if err != nil {
			return nil, fmt.Errorf("reading the CA file: %w", err)
		}

		opts = append(opts, commypanel.WithCACertPEM(pem))
	}

	return commypanel.New(s.url, s.token, opts...)
}

// describe turns an error into a sentence for a person, with what to do about it.
func (a *app) describe(err error) string {
	var apiErr *commypanel.APIError

	switch {
	case errors.As(err, &apiErr):
		switch apiErr.Status {
		case 401:
			return "the token is not valid (wrong, revoked or expired). Create a new one in the panel and run `commy login` again."
		case 403:
			return "this token is not allowed to do that (" + apiErr.Message + "). Make a token with the ability it needs."
		case 404:
			return "not found: no such app, deployment or variable, or this token may not use that app."
		case 429:
			if apiErr.RetryAfterSeconds > 0 {
				return fmt.Sprintf("too many calls; try again in %d seconds.", apiErr.RetryAfterSeconds)
			}

			return "too many calls; try again in a minute."
		default:
			return plainAPIError(apiErr)
		}
	case errors.Is(err, commypanel.ErrInsecureURL):
		return "the panel address must be https. (For a panel on this machine only, add --allow-http-localhost.)"
	case errors.Is(err, commypanel.ErrRedirect):
		return err.Error()
	case errors.Is(err, context.Canceled):
		return "cancelled."
	}

	return err.Error()
}

// plainAPIError is the panel's sentence and, for a validation error, what was wrong with which field.
func plainAPIError(e *commypanel.APIError) string {
	msg := e.Message

	names := make([]string, 0, len(e.Fields))
	for name := range e.Fields {
		names = append(names, name)
	}

	sort.Strings(names)

	for _, name := range names {
		msg += fmt.Sprintf(" (%s: %s)", name, strings.Join(e.Fields[name], " "))
	}

	return msg
}

func (a *app) help() {
	fmt.Fprint(a.err(), `commy controls a CommyPanel server through its API.

Usage: commy <command> [arguments]

  login [--url <address>]    sign in: asks for the address and the token (typed hidden), checks it, saves it
  logout                     forget the saved login
  whoami                     what the token is and may do
  apps [list|show <app>]     list apps, or show one
  deploy <app>               deploy and wait for the result   (--no-wait, --timeout)
  apply <app>                apply changed variables, volumes and links
  deployments <app>          the latest deployments
  deployment <app> <number>  one deployment                   (--log)
  start|stop|restart <app>   control a running app
  logs <app>                 the last lines of its output     (--tail)
  env list <app>             the names of its variables (values are never shown)
  env set <app> <KEY>        set a variable; the value is read from stdin (or --from-file)
  env unset <app> <KEY>      remove a variable
  update                     look for a newer commy and install it   (--check, --yes)
  version

Every command accepts --url, --ca-file and --allow-http-localhost; most accept --json.
The token is never taken as an argument: use "commy login" or set $COMMY_TOKEN.
Exit codes: 0 ok, 1 error, 2 usage, 3 a deployment failed or was rolled back, 4 still running when --timeout ended.
`)
}
