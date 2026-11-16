package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	commypanel "github.com/commypanel/commypanel-go"
)

// safe removes what could act on a terminal. Text from an app's logs, a commit message or the panel's own
// error sentence is not under the user's control, and a program that prints control characters (escape
// sequences) can retitle a window, hide text or put something on the clipboard. Tabs and line
// breaks stay; every other control character is shown as a visible replacement.
func safe(s string) string {
	var b strings.Builder

	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r == '\r':
			// A carriage return can overwrite what was just printed.
			b.WriteString("\n")
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) || r == 0x2028 || r == 0x2029 || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069):
			b.WriteString("�")
		default:
			b.WriteRune(r)
		}
	}

	return b.String()
}

func printJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")

	return enc.Encode(v)
}

func table(w io.Writer, rows [][]string) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)

	for _, row := range rows {
		for i := range row {
			row[i] = safe(row[i])
		}

		fmt.Fprintln(tw, strings.Join(row, "\t"))
	}

	_ = tw.Flush()
}

func str(p *string, otherwise string) string {
	if p == nil || *p == "" {
		return otherwise
	}

	return *p
}

func when(t *time.Time) string {
	if t == nil {
		return "-"
	}

	return t.Local().Format("2006-01-02 15:04")
}

func modeLabel(s commypanel.DeployStrategy) string {
	if s == commypanel.StrategyStopFirst {
		return "stop the old version first"
	}

	return "zero downtime"
}

func deploymentRow(d commypanel.Deployment) []string {
	kind := "deploy"
	if d.IsRollback {
		kind = "rollback"
	}

	return []string{"#" + fmt.Sprint(d.Number), d.Status, kind, str(d.Commit, "-"), when(d.CreatedAt)}
}

func printApp(w io.Writer, app commypanel.App) {
	live := "-"
	if app.LiveDeployment != nil {
		live = "#" + fmt.Sprint(*app.LiveDeployment)
	}

	table(w, [][]string{
		{"App:", app.Name + " (" + app.Slug + ")"},
		{"Status:", app.Status},
		{"Type:", str(app.Stack, "-")},
		{"Deploy mode:", modeLabel(app.DeployStrategy)},
		{"Live release:", live},
		{"Domains:", strings.Join(app.Domains, ", ")},
	})
}

func printDeployment(w io.Writer, app string, d commypanel.Deployment) {
	rows := [][]string{
		{"Deployment:", app + " #" + fmt.Sprint(d.Number)},
		{"Status:", d.Status},
		{"Stage:", str(d.Stage, "-")},
		{"Mode:", modeLabel(d.Strategy)},
		{"Commit:", str(d.Commit, "-") + " " + str(d.CommitMessage, "")},
		{"Started:", when(d.CreatedAt)},
		{"Finished:", when(d.FinishedAt)},
	}

	if d.Error != nil && *d.Error != "" {
		rows = append(rows, []string{"Error:", *d.Error})
	}

	table(w, rows)
}
