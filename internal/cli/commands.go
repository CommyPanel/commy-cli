package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	commypanel "github.com/commypanel/commypanel-go"
)

const maxEnvValue = 32768

func one(positional []string, what string) (string, error) {
	if len(positional) != 1 {
		return "", usagef("expected %s", what)
	}

	return positional[0], nil
}

func (a *app) login(args []string) error {
	fs, c := a.flags("login", "login --url <address> [--ca-file file]   (the token comes from $"+envToken+")")
	c.register(fs, false)

	pos, err := parse(fs, args)
	if err != nil {
		return err
	}

	if len(pos) != 0 {
		return usagef("login takes no arguments")
	}

	token := a.env.Getenv(envToken)
	if token == "" {
		return usagef("set %s to the token first, so it never appears in a command line or the shell history, for example:\n  read -rs %s; export %s\n  commy login --url https://panel.example.com", envToken, envToken, envToken)
	}

	url := firstNonEmpty(c.url, a.env.Getenv(envURL))
	if url == "" {
		return usagef("give the panel address: commy login --url https://panel.example.com")
	}

	client, err := a.newClient(settings{url: url, token: token, caFile: c.caFile}, c)
	if err != nil {
		return err
	}

	info, err := client.Token(a.ctx)
	if err != nil {
		return err
	}

	path, err := configPath(a.env.Getenv)
	if err != nil {
		return err
	}

	if err := saveConfig(path, config{URL: client.BaseURL(), Token: token, CAFile: c.caFile}); err != nil {
		return fmt.Errorf("saving the login: %w", err)
	}

	fmt.Fprintf(a.out(), "Signed in to %s with the token %q (owner %s). Saved to %s, readable only by you.\n", client.BaseURL(), safe(info.Name), safe(info.Owner), path)
	a.printToken(info, false)

	return nil
}

func (a *app) logout(args []string) error {
	fs, _ := a.flags("logout", "logout")

	if _, err := parse(fs, args); err != nil {
		return err
	}

	path, err := configPath(a.env.Getenv)
	if err != nil {
		return err
	}

	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	fmt.Fprintln(a.out(), "The saved login is gone. (The token itself still works until you revoke it in the panel.)")

	return nil
}

func (a *app) whoami(args []string) error {
	fs, c := a.flags("whoami", "whoami [--json]")
	c.register(fs, true)

	if _, err := parse(fs, args); err != nil {
		return err
	}

	client, err := a.client(c)
	if err != nil {
		return err
	}

	info, err := client.Token(a.ctx)
	if err != nil {
		return err
	}

	if c.json {
		return printJSON(a.out(), info)
	}

	fmt.Fprintf(a.out(), "Panel: %s\n", client.BaseURL())
	a.printToken(info, true)

	return nil
}

func (a *app) printToken(info *commypanel.Token, withName bool) {
	abilities := make([]string, len(info.Abilities))
	for i, ability := range info.Abilities {
		abilities[i] = string(ability)
	}

	apps := "every app its owner may use"
	if info.Apps != nil {
		apps = strings.Join(info.Apps, ", ")
	}

	rows := [][]string{}

	if withName {
		rows = append(rows, []string{"Token:", info.Name}, []string{"Owner:", info.Owner})
	}

	rows = append(rows,
		[]string{"May:", strings.Join(abilities, ", ")},
		[]string{"Apps:", apps},
		[]string{"Expires:", expiry(info.ExpiresAt)},
	)

	table(a.out(), rows)
}

func expiry(t *time.Time) string {
	if t == nil {
		return "never (rotate it regularly)"
	}

	return when(t)
}

func (a *app) apps(args []string) error {
	sub := "list"

	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}

	switch sub {
	case "list":
		return a.appsList(args)
	case "show":
		return a.appsShow(args)
	default:
		return usagef("unknown: apps %s (use: apps list, apps show <app>)", sub)
	}
}

func (a *app) appsList(args []string) error {
	fs, c := a.flags("apps list", "apps list [--json]")
	c.register(fs, true)

	if _, err := parse(fs, args); err != nil {
		return err
	}

	client, err := a.client(c)
	if err != nil {
		return err
	}

	apps, err := client.ListApps(a.ctx)
	if err != nil {
		return err
	}

	if c.json {
		return printJSON(a.out(), apps)
	}

	rows := [][]string{{"APP", "NAME", "STATUS", "STACK", "LIVE", "DOMAINS"}}

	for _, app := range apps {
		live := "-"
		if app.LiveDeployment != nil {
			live = fmt.Sprintf("#%d", *app.LiveDeployment)
		}

		rows = append(rows, []string{app.Slug, app.Name, app.Status, str(app.Stack, "-"), live, strings.Join(app.Domains, ",")})
	}

	table(a.out(), rows)

	return nil
}

func (a *app) appsShow(args []string) error {
	fs, c := a.flags("apps show", "apps show <app> [--json]")
	c.register(fs, true)

	pos, err := parse(fs, args)
	if err != nil {
		return err
	}

	slug, err := one(pos, "one app name")
	if err != nil {
		return err
	}

	client, err := a.client(c)
	if err != nil {
		return err
	}

	app, err := client.GetApp(a.ctx, slug)
	if err != nil {
		return err
	}

	if c.json {
		return printJSON(a.out(), app)
	}

	printApp(a.out(), *app)

	if d := app.LatestDeployment; d != nil {
		fmt.Fprintln(a.out())
		printDeployment(a.out(), app.Slug, *d)
	}

	return nil
}

// deploy starts a deployment (or, with apply, applies changed variables, volumes and links) and waits for it.
func (a *app) deploy(args []string, apply bool) error {
	name := "deploy"
	if apply {
		name = "apply"
	}

	fs, c := a.flags(name, name+" <app> [--no-wait] [--timeout 15m] [--json]")
	c.register(fs, true)
	noWait := fs.Bool("no-wait", false, "queue it and return at once")
	timeout := fs.Duration("timeout", 15*time.Minute, "how long to wait for the result")
	poll := fs.Duration("poll", 3*time.Second, "how often to look (at least 1s)")

	pos, err := parse(fs, args)
	if err != nil {
		return err
	}

	slug, err := one(pos, "one app name")
	if err != nil {
		return err
	}

	if *timeout <= 0 || *poll < time.Second {
		return usagef("--timeout must be positive and --poll at least 1s")
	}

	client, err := a.client(c)
	if err != nil {
		return err
	}

	var queued *commypanel.Deployment

	if apply {
		queued, err = client.ApplyChanges(a.ctx, slug)
	} else {
		queued, err = client.StartDeployment(a.ctx, slug)
	}

	if err != nil {
		var apiErr *commypanel.APIError
		if errors.As(err, &apiErr) && apiErr.Status == 409 && apiErr.Deployment != nil {
			return fmt.Errorf("%s Follow it with: commy deployment %s %d", apiErr.Message, slug, apiErr.Deployment.Number)
		}

		return err
	}

	if *noWait {
		if c.json {
			return printJSON(a.out(), queued)
		}

		fmt.Fprintf(a.out(), "Queued %s #%d. Follow it with: commy deployment %s %d\n", slug, queued.Number, slug, queued.Number)

		return nil
	}

	ctx, cancel := context.WithTimeout(a.ctx, *timeout)
	defer cancel()

	var last string

	final, err := client.WaitForDeployment(ctx, slug, queued.Number, *poll, func(d *commypanel.Deployment) {
		state := d.Status + " " + str(d.Stage, "")
		if state != last {
			last = state
			fmt.Fprintf(a.err(), "%s #%d: %s", slug, d.Number, safe(d.Status))

			if d.Stage != nil {
				fmt.Fprintf(a.err(), " (%s)", safe(*d.Stage))
			}

			fmt.Fprintln(a.err())
		}
	})
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return exitError{ExitWaitTimedOut, fmt.Sprintf("still running after %s. It carries on in the panel; follow it with: commy deployment %s %d", *timeout, slug, queued.Number)}
		}

		return err
	}

	if c.json {
		if err := printJSON(a.out(), final); err != nil {
			return err
		}
	} else {
		printDeployment(a.out(), slug, *final)
	}

	if !final.Succeeded {
		reason := str(final.Error, "see the log with: commy deployment "+slug+" "+fmt.Sprint(final.Number)+" --log")

		return exitError{ExitDeployFailed, fmt.Sprintf("the deployment ended as %s: %s", final.Status, reason)}
	}

	return nil
}

func (a *app) deployments(args []string) error {
	fs, c := a.flags("deployments", "deployments <app> [--limit N] [--json]")
	c.register(fs, true)
	limit := fs.Int("limit", 0, "how many (the panel allows up to 50)")

	pos, err := parse(fs, args)
	if err != nil {
		return err
	}

	slug, err := one(pos, "one app name")
	if err != nil {
		return err
	}

	client, err := a.client(c)
	if err != nil {
		return err
	}

	list, err := client.ListDeployments(a.ctx, slug, *limit)
	if err != nil {
		return err
	}

	if c.json {
		return printJSON(a.out(), list)
	}

	rows := [][]string{{"DEPLOYMENT", "STATUS", "TYPE", "COMMIT", "STARTED"}}

	for _, d := range list {
		rows = append(rows, deploymentRow(d))
	}

	table(a.out(), rows)

	return nil
}

func (a *app) deployment(args []string) error {
	fs, c := a.flags("deployment", "deployment <app> <number> [--log] [--json]")
	c.register(fs, true)
	withLog := fs.Bool("log", false, "include the build log")

	pos, err := parse(fs, args)
	if err != nil {
		return err
	}

	if len(pos) != 2 {
		return usagef("expected an app name and a deployment number")
	}

	number, err := strconv.Atoi(strings.TrimPrefix(pos[1], "#"))
	if err != nil || number < 1 {
		return usagef("%q is not a deployment number", pos[1])
	}

	client, err := a.client(c)
	if err != nil {
		return err
	}

	d, err := client.GetDeployment(a.ctx, pos[0], number, *withLog)
	if err != nil {
		return err
	}

	if c.json {
		return printJSON(a.out(), d)
	}

	printDeployment(a.out(), pos[0], *d)

	if *withLog {
		fmt.Fprintf(a.out(), "\n%s\n", safe(d.Log))
	}

	return nil
}

func (a *app) control(action string, args []string) error {
	fs, c := a.flags(action, action+" <app> [--json]")
	c.register(fs, true)

	pos, err := parse(fs, args)
	if err != nil {
		return err
	}

	slug, err := one(pos, "one app name")
	if err != nil {
		return err
	}

	client, err := a.client(c)
	if err != nil {
		return err
	}

	var result *commypanel.App

	switch action {
	case "start":
		result, err = client.StartApp(a.ctx, slug)
	case "stop":
		result, err = client.StopApp(a.ctx, slug)
	default:
		result, err = client.RestartApp(a.ctx, slug)
	}

	if err != nil {
		return err
	}

	if c.json {
		return printJSON(a.out(), result)
	}

	fmt.Fprintf(a.out(), "%s: %s\n", result.Slug, safe(result.Status))

	return nil
}

func (a *app) logs(args []string) error {
	fs, c := a.flags("logs", "logs <app> [--tail N] [--json]")
	c.register(fs, true)
	tail := fs.Int("tail", 0, "how many lines (10 to 5000, default 200)")

	pos, err := parse(fs, args)
	if err != nil {
		return err
	}

	slug, err := one(pos, "one app name")
	if err != nil {
		return err
	}

	client, err := a.client(c)
	if err != nil {
		return err
	}

	logs, err := client.Logs(a.ctx, slug, *tail)
	if err != nil {
		return err
	}

	if c.json {
		return printJSON(a.out(), logs)
	}

	if !logs.Live {
		fmt.Fprintln(a.err(), "commy: the app has no running container, so there are no logs.")

		return nil
	}

	// What an app prints is not under our control: control characters are made visible, not sent to the terminal.
	_, err = io.WriteString(a.out(), safe(logs.Logs))

	return err
}

func (a *app) envCommand(args []string) error {
	if len(args) == 0 {
		return usagef("use: env list <app>, env set <app> <KEY>, env unset <app> <KEY>")
	}

	switch args[0] {
	case "list":
		return a.envList(args[1:])
	case "set":
		return a.envSet(args[1:])
	case "unset":
		return a.envUnset(args[1:])
	default:
		return usagef("unknown: env %s (use: list, set, unset)", args[0])
	}
}

func (a *app) envList(args []string) error {
	fs, c := a.flags("env list", "env list <app> [--json]")
	c.register(fs, true)

	pos, err := parse(fs, args)
	if err != nil {
		return err
	}

	slug, err := one(pos, "one app name")
	if err != nil {
		return err
	}

	client, err := a.client(c)
	if err != nil {
		return err
	}

	keys, err := client.ListEnv(a.ctx, slug)
	if err != nil {
		return err
	}

	if c.json {
		return printJSON(a.out(), keys)
	}

	for _, key := range keys.Keys {
		fmt.Fprintln(a.out(), safe(key))
	}

	if keys.PendingChanges {
		fmt.Fprintf(a.err(), "commy: changes are waiting to be applied: commy apply %s\n", slug)
	}

	return nil
}

func (a *app) envSet(args []string) error {
	fs, c := a.flags("env set", "env set <app> <KEY> [--from-file path]   (the value is read from stdin)")
	c.register(fs, true)
	fromFile := fs.String("from-file", "", "read the value from this file instead of stdin")

	pos, err := parse(fs, args)
	if err != nil {
		return err
	}

	if len(pos) != 2 {
		return usagef("expected an app name and a variable name; the value is read from stdin, never from the command line")
	}

	value, err := a.readValue(*fromFile)
	if err != nil {
		return err
	}

	client, err := a.client(c)
	if err != nil {
		return err
	}

	change, created, err := client.SetEnv(a.ctx, pos[0], pos[1], value)
	if err != nil {
		return err
	}

	if c.json {
		return printJSON(a.out(), map[string]any{"key": change.Key, "created": created, "pending_changes": change.PendingChanges})
	}

	verb := "updated"
	if created {
		verb = "created"
	}

	fmt.Fprintf(a.out(), "%s %s.", safe(change.Key), verb)

	if change.PendingChanges {
		fmt.Fprintf(a.out(), " It takes effect with: commy apply %s", pos[0])
	}

	fmt.Fprintln(a.out())

	return nil
}

// readValue reads a variable's value from a file or from piped input. It refuses a terminal: typing a
// secret into a visible prompt, or onto the command line, is how secrets end up in histories.
func (a *app) readValue(fromFile string) (string, error) {
	var reader io.Reader

	if fromFile != "" {
		f, err := os.Open(fromFile)
		if err != nil {
			return "", err
		}

		defer f.Close()

		reader = f
	} else {
		if a.env.interactive() {
			return "", usagef("pipe the value in, so it is not typed where it can be seen or kept: printf %%s \"$VALUE\" | commy env set <app> <KEY>   (or use --from-file)")
		}

		reader = a.env.Stdin
	}

	raw, err := io.ReadAll(io.LimitReader(reader, maxEnvValue+3))
	if err != nil {
		return "", err
	}

	value := strings.TrimSuffix(strings.TrimSuffix(string(raw), "\n"), "\r")

	if len(value) > maxEnvValue {
		return "", fmt.Errorf("the value is longer than %d bytes", maxEnvValue)
	}

	return value, nil
}

func (a *app) envUnset(args []string) error {
	fs, c := a.flags("env unset", "env unset <app> <KEY> [--json]")
	c.register(fs, true)

	pos, err := parse(fs, args)
	if err != nil {
		return err
	}

	if len(pos) != 2 {
		return usagef("expected an app name and a variable name")
	}

	client, err := a.client(c)
	if err != nil {
		return err
	}

	change, err := client.UnsetEnv(a.ctx, pos[0], pos[1])
	if err != nil {
		return err
	}

	if c.json {
		return printJSON(a.out(), change)
	}

	fmt.Fprintf(a.out(), "%s removed.", safe(change.Key))

	if change.PendingChanges {
		fmt.Fprintf(a.out(), " It stops being used with: commy apply %s", pos[0])
	}

	fmt.Fprintln(a.out())

	return nil
}
