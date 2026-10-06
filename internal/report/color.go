package report

import "fmt"

// Painter adds ANSI colors when On. Padding is applied before coloring so
// escape codes never break column alignment.
type Painter struct{ On bool }

func (p Painter) wrap(code, s string) string {
	if !p.On || s == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func (p Painter) Bold(s string) string    { return p.wrap("1", s) }
func (p Painter) Dim(s string) string     { return p.wrap("2", s) }
func (p Painter) Red(s string) string     { return p.wrap("31", s) }
func (p Painter) Green(s string) string   { return p.wrap("32", s) }
func (p Painter) Yellow(s string) string  { return p.wrap("33", s) }
func (p Painter) Blue(s string) string    { return p.wrap("34", s) }
func (p Painter) Magenta(s string) string { return p.wrap("35", s) }
func (p Painter) Cyan(s string) string    { return p.wrap("36", s) }

func (p Painter) Status(s string, width int) string {
	padded := fmt.Sprintf("%-*s", width, s)
	switch s {
	case "DEAD", "BREAKING":
		return p.Red(padded)
	case "CONDITIONAL", "PINNED":
		return p.Magenta(padded)
	case "REDUNDANT", "CHANGED":
		return p.Yellow(padded)
	case "ALIVE", "FIXED":
		return p.Green(padded)
	case "GROUP":
		return p.Cyan(padded)
	case "ERROR":
		return p.Blue(padded)
	case "SHARED":
		return p.Dim(padded)
	default:
		return padded
	}
}

// mdStatus is the status cell of a Markdown table: the statuses that fail by
// default are bold, the others plain.
func mdStatus(status string) string {
	switch status {
	case "DEAD", "BREAKING":
		return "**" + status + "**"
	default:
		return status
	}
}
