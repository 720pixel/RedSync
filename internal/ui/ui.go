package ui

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// RedSync palette. red is the brand, the rest are accents for a dark terminal.
var (
	white  = lipgloss.Color("#F5F5F5")
	gray   = lipgloss.Color("#7A7A7A")
	green  = lipgloss.Color("#3DDC84")
	yellow = lipgloss.Color("#FFC542")
	cyan   = lipgloss.Color("#34E2E2")
	red    = lipgloss.Color("#FF3B3B")
	redDim = lipgloss.Color("#8A0A0A")

	// vertical red gradient for the wordmark, bright at the top.
	logoShades = []lipgloss.Color{"#FF6B6B", "#FF4040", "#F02222", "#CE1414", "#A50E0E", "#7E0808"}
)

var (
	Brand  = lipgloss.NewStyle().Foreground(red).Bold(true)
	Accent = lipgloss.NewStyle().Foreground(cyan)
	Muted  = lipgloss.NewStyle().Foreground(gray)
	Title  = lipgloss.NewStyle().Foreground(white).Bold(true)

	okStyle  = lipgloss.NewStyle().Foreground(green).Bold(true)
	warnSty  = lipgloss.NewStyle().Foreground(yellow).Bold(true)
	errSty   = lipgloss.NewStyle().Foreground(red).Bold(true)
	stepSty  = lipgloss.NewStyle().Foreground(cyan).Bold(true)
	bracket  = lipgloss.NewStyle().Foreground(gray)
	barStyle = lipgloss.NewStyle().Foreground(red).Bold(true)

	pill = lipgloss.NewStyle().Foreground(white).Background(redDim).Padding(0, 1).Bold(true)
	box  = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(red).
		Padding(0, 2)
)

// Logo is the wordmark, each row a slightly darker red for a bit of depth.
func Logo() string {
	art := []string{
		`██████╗ ███████╗██████╗ ███████╗██╗   ██╗███╗   ██╗ ██████╗`,
		`██╔══██╗██╔════╝██╔══██╗██╔════╝╚██╗ ██╔╝████╗  ██║██╔════╝`,
		`██████╔╝█████╗  ██║  ██║███████╗ ╚████╔╝ ██╔██╗ ██║██║     `,
		`██╔══██╗██╔══╝  ██║  ██║╚════██║  ╚██╔╝  ██║╚██╗██║██║     `,
		`██║  ██║███████╗██████╔╝███████║   ██║   ██║ ╚████║╚██████╗`,
		`╚═╝  ╚═╝╚══════╝╚═════╝ ╚══════╝   ╚═╝   ╚═╝  ╚═══╝ ╚═════╝`,
	}
	var b strings.Builder
	for i, line := range art {
		b.WriteString(lipgloss.NewStyle().Foreground(logoShades[i%len(logoShades)]).Bold(true).Render(line))
		b.WriteByte('\n')
	}
	return b.String()
}

// Banner is the header on a bare run or -h: wordmark, a framed strapline, and a
// thin rule to seat it like a proper tool splash.
func Banner(version string) string {
	strap := Muted.Render("multi-source sync") + Brand.Render("  ·  ") +
		Muted.Render("dolby vision / hdr hybrids") + Brand.Render("  ·  ") +
		pill.Render("v"+version)
	framed := box.Render(strap)

	var b strings.Builder
	b.WriteByte('\n')
	b.WriteString(Logo())
	b.WriteByte('\n')
	b.WriteString(framed)
	b.WriteByte('\n')
	b.WriteString(rule())
	b.WriteByte('\n')
	return b.String()
}

func rule() string {
	return lipgloss.NewStyle().Foreground(redDim).Render(strings.Repeat("─", 60))
}

// quiet silences the decorative output (steps, sections, fields, spinners) so a
// caller wiring RedSync into a script - especially with --json - gets clean
// streams. Errors always print; only the cosmetics are suppressed.
var quiet bool

// SetQuiet turns the decorative output on or off. Call it once at startup.
func SetQuiet(b bool) { quiet = b }

// Quiet reports whether decorative output is suppressed.
func Quiet() bool { return quiet }

// status lines. all to stderr so piping stdout stays clean. bracketed glyphs for
// that terminal-tool feel.
func tag(style lipgloss.Style, glyph string) string {
	return bracket.Render("[") + style.Render(glyph) + bracket.Render("]")
}

func Step(msg string) {
	if quiet {
		return
	}
	fmt.Fprintln(os.Stderr, tag(stepSty, "*")+" "+msg)
}
func OK(msg string) {
	if quiet {
		return
	}
	fmt.Fprintln(os.Stderr, tag(okStyle, "✓")+" "+msg)
}
func Warn(msg string) {
	if quiet {
		return
	}
	fmt.Fprintln(os.Stderr, tag(warnSty, "!")+" "+Muted.Render(msg))
}
func Err(msg string) { fmt.Fprintln(os.Stderr, tag(errSty, "✗")+" "+msg) }

// Section is a bold red bar plus an uppercase heading, like a tool's stage label.
func Section(title string) {
	if quiet {
		return
	}
	fmt.Fprintln(os.Stderr, "\n"+barStyle.Render("▌ ")+Title.Render(strings.ToUpper(title)))
}

// Field prints a dim label and a value, lined up.
func Field(label, value string) {
	if quiet {
		return
	}
	if label == "" {
		fmt.Fprintf(os.Stderr, "    %s\n", value)
		return
	}
	fmt.Fprintf(os.Stderr, "  %s %s\n", Muted.Render(fmt.Sprintf("%-13s", label)), value)
}

func Pill(s string) string { return pill.Render(s) }
