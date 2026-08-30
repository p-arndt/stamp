// Package ui renders stamp's terminal output: the release plan, the preflight
// check list, and the confirmation prompt.
//
// Everything here writes plain lines with lipgloss styling. There is no TUI:
// a release is a one-shot, scriptable operation, and its output has to stay
// readable when it is piped into a log or a CI job.
package ui

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-isatty"
)

// Out and Err are indirected so tests can capture output.
var (
	Out io.Writer = os.Stdout
	Err io.Writer = os.Stderr
)

// The palette sticks to the 16 ANSI colors so it inherits the user's terminal
// theme instead of fighting it. lipgloss downgrades to no color automatically
// when the output is not a terminal or NO_COLOR is set.
var (
	titleStyle = lipgloss.NewStyle().Bold(true)
	labelStyle = lipgloss.NewStyle().Faint(true)
	goodStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	badStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	warnStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	dimStyle   = lipgloss.NewStyle().Faint(true)
	bumpStyle  = lipgloss.NewStyle().Bold(true)
)

// labelWidth aligns the value column of Field lines.
const labelWidth = 14

// Title prints a blank line followed by a bold heading.
func Title(format string, a ...any) {
	fmt.Fprintf(Out, "\n%s\n\n", titleStyle.Render(fmt.Sprintf(format, a...)))
}

// Field prints an aligned "label   value" line.
func Field(label, value string) {
	fmt.Fprintf(Out, "%s%s\n", labelStyle.Render(pad(label, labelWidth)), value)
}

// Bump renders a "0.4.0 → 0.5.0" transition.
func Bump(from, to string) string {
	return fmt.Sprintf("%s %s %s", from, dimStyle.Render("→"), bumpStyle.Render(to))
}

// Section prints a blank line and a section header such as "Checks:".
func Section(name string) {
	fmt.Fprintf(Out, "\n%s\n", name)
}

// Item prints an indented plain list entry.
func Item(text string) {
	fmt.Fprintf(Out, "  %s\n", text)
}

// Pass, Fail and Skip print an indented check result.
func Pass(text string) { fmt.Fprintf(Out, "  %s %s\n", goodStyle.Render("✓"), text) }
func Fail(text string) { fmt.Fprintf(Out, "  %s %s\n", badStyle.Render("✗"), text) }
func Skip(text string) {
	fmt.Fprintf(Out, "  %s %s\n", warnStyle.Render("-"), dimStyle.Render(text))
}

// Step prints a progress line for an action that is being carried out.
func Step(format string, a ...any) {
	fmt.Fprintf(Out, "%s\n", fmt.Sprintf(format, a...))
}

// Note prints a dimmed informational line.
func Note(format string, a ...any) {
	fmt.Fprintf(Out, "%s\n", dimStyle.Render(fmt.Sprintf(format, a...)))
}

// Blank prints an empty line.
func Blank() { fmt.Fprintln(Out) }

// Errorf prints an error to stderr in stamp's "error: …" form.
func Errorf(format string, a ...any) {
	fmt.Fprintf(Err, "%s %s\n", badStyle.Render("error:"), fmt.Sprintf(format, a...))
}

// Hint prints an indented follow-up suggestion under an error, usually a
// command the user can copy.
func Hint(format string, a ...any) {
	fmt.Fprintf(Err, "  %s\n", dimStyle.Render(fmt.Sprintf(format, a...)))
}

// Interactive reports whether stdin is a terminal, i.e. whether there is
// anybody there to answer a question.
func Interactive() bool {
	return isatty.IsTerminal(os.Stdin.Fd()) || isatty.IsCygwinTerminal(os.Stdin.Fd())
}

// Confirm asks a yes/no question, defaulting to no. It reports an error when
// stdin is not a terminal: a release must never proceed on an unanswered
// prompt just because nobody was there to answer it.
func Confirm(question string) (bool, error) {
	if !Interactive() {
		return false, fmt.Errorf("not a terminal, pass --yes to confirm non-interactively")
	}
	return ConfirmDefault(question, false)
}

// ConfirmDefault asks a yes/no question with the given default, for the
// questions `stamp init` asks. Unlike Confirm it does not insist on a terminal;
// a caller that has no terminal simply gets the default, which is what makes
// `stamp init` usable in a script.
func ConfirmDefault(question string, def bool) (bool, error) {
	hint := "[y/N]"
	if def {
		hint = "[Y/n]"
	}
	if !Interactive() {
		return def, nil
	}
	fmt.Fprintf(Out, "\n%s %s ", question, dimStyle.Render(hint))
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return def, nil // EOF (e.g. ctrl-d) takes the default.
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	default:
		return def, nil
	}
}

// Ask puts a free-text question with a default, and returns the default when
// the answer is empty or nobody is there to type one.
func Ask(question, def string) string {
	if !Interactive() {
		return def
	}
	fmt.Fprintf(Out, "%s %s ", question, dimStyle.Render("["+def+"]"))
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return def
	}
	if answer := strings.TrimSpace(line); answer != "" {
		return answer
	}
	return def
}

func pad(s string, width int) string {
	if len(s) >= width {
		return s + " "
	}
	return s + strings.Repeat(" ", width-len(s))
}
