package main

import (
	"bufio"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	reset      = "\x1b[0m"
	dim        = "\x1b[2m"
	tsFormat   = "15:04:05.000"
	tsWidth    = 12
	levelWidth = 7
)

var palette = []string{
	"\x1b[36m", // cyan
	"\x1b[32m", // green
	"\x1b[33m", // yellow
	"\x1b[35m", // magenta
	"\x1b[34m", // blue
	"\x1b[31m", // red
	"\x1b[96m", // bright cyan
	"\x1b[92m", // bright green
	"\x1b[93m", // bright yellow
	"\x1b[95m", // bright magenta
	"\x1b[94m", // bright blue
	"\x1b[91m", // bright red
}

type renderer struct {
	w          *bufio.Writer
	tty        *os.File // non-nil when writing to a terminal; used to track width
	color      bool
	aliasWidth int
	msgCol     int
	avail      int // usable columns right of the gutter; 0 disables wrapping
	gutter     string
	svcColor   map[string]string
}

func newRenderer(w io.Writer, services []Service, color bool) *renderer {
	r := &renderer{w: bufio.NewWriter(w), color: color, svcColor: make(map[string]string, len(services))}
	for i, svc := range services {
		if len(svc.Alias) > r.aliasWidth {
			r.aliasWidth = len(svc.Alias)
		}
		if color {
			r.svcColor[svc.Alias] = palette[i%len(palette)]
		}
	}
	r.msgCol = tsWidth + 1 + r.aliasWidth + 1 + levelWidth + 1
	if f, ok := w.(*os.File); ok && termWidth(f) > 0 {
		r.tty = f
	}
	if color {
		r.gutter = strings.Repeat(" ", r.msgCol-2) + dim + "│" + reset + " "
	} else {
		r.gutter = strings.Repeat(" ", r.msgCol-2) + "│ "
	}
	return r
}

func (r *renderer) print(e *Entry) {
	r.updateWidth()
	lvl := levelColor(e.Level)
	r.w.WriteString(time.UnixMilli(e.EpochMS).Format(tsFormat))
	r.w.WriteByte(' ')
	r.w.WriteString(r.field(e.Service, r.svcColor[e.Service], r.aliasWidth))
	r.w.WriteByte(' ')
	r.w.WriteString(r.field(e.Level, lvl, levelWidth))
	r.w.WriteByte(' ')
	for i, chunk := range r.chunks(e.Message) {
		if i > 0 {
			r.w.WriteString(r.gutter)
		}
		r.w.WriteString(r.colorize(chunk, lvl))
		r.w.WriteByte('\n')
	}
	for _, line := range normalizeBlock(e.Lines[1:]) {
		for _, chunk := range r.chunks(line) {
			r.w.WriteString(r.gutter)
			r.w.WriteString(r.colorize(chunk, lvl))
			r.w.WriteByte('\n')
		}
	}
	// flush per entry: without this, output sits in the 4KB buffer and appears
	// to stall mid-entry (or mid-word) until later entries push it out
	r.w.Flush()
}

// updateWidth refreshes the wrap width from the terminal, so wrapping tracks
// pane resizes (and the real size once a detached tmux session is attached).
func (r *renderer) updateWidth() {
	r.avail = 0
	if r.tty == nil {
		return
	}
	if width := termWidth(r.tty); width-r.msgCol >= 20 {
		r.avail = width - r.msgCol
	}
}

// chunks splits s into pieces that fit right of the gutter, so long lines wrap
// at the message column instead of the terminal edge. Counted in runes, not
// bytes, so multi-byte characters are never split at the wrap point.
func (r *renderer) chunks(s string) []string {
	if r.avail <= 0 {
		return []string{s}
	}
	var out []string
	start, count := 0, 0
	for i := range s {
		if count == r.avail {
			out = append(out, s[start:i])
			start, count = i, 0
		}
		count++
	}
	return append(out, s[start:])
}

// normalizeBlock cleans continuation lines so they sit at the message column:
// tabs become spaces, trailing \r is dropped, and the block's common leading
// indentation is removed while relative nesting is preserved.
func normalizeBlock(lines []string) []string {
	if len(lines) == 0 {
		return nil
	}
	out := make([]string, len(lines))
	minIndent := -1
	for i, line := range lines {
		line = strings.TrimRight(strings.ReplaceAll(line, "\t", "    "), "\r")
		out[i] = line
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if minIndent < 0 || indent < minIndent {
			minIndent = indent
		}
	}
	if minIndent > 0 {
		for i, line := range out {
			if len(line) >= minIndent {
				out[i] = line[minIndent:]
			}
		}
	}
	return out
}

// banner prints a startup summary so an empty window isn't mistaken for a stall.
func (r *renderer) banner(mode string, services []Service, filters string) {
	names := make([]string, len(services))
	for i, svc := range services {
		names[i] = r.colorize(svc.Alias, r.svcColor[svc.Alias])
	}
	line := "flowlog " + mode + " " + strings.Join(names, ", ")
	if filters != "" {
		line += r.colorize(" ("+filters+")", dim)
	}
	if r.tty != nil {
		line += r.colorize(" [cols "+strconv.Itoa(termWidth(r.tty))+"]", dim)
	} else {
		line += r.colorize(" [width unknown, wrapping off]", dim)
	}
	r.w.WriteString(line + "\n")
	r.w.Flush()
}

func (r *renderer) field(s string, code string, width int) string {
	pad := width - len(s)
	if pad < 0 {
		pad = 0
	}
	return r.colorize(s, code) + strings.Repeat(" ", pad)
}

func (r *renderer) colorize(s string, code string) string {
	if !r.color || code == "" {
		return s
	}
	return code + s + reset
}

func (r *renderer) flush() { r.w.Flush() }

func levelColor(level string) string {
	switch level {
	case "error":
		return "\x1b[31m"
	case "warn":
		return "\x1b[33m"
	case "info":
		return "\x1b[32m"
	case "verbose":
		return "\x1b[36m"
	case "silly":
		return "\x1b[35m"
	default:
		return ""
	}
}
