// Package logx は標準出力と /denpa/logs の両方に出す記録 (仕様書 14)。
package logx

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// MaxLines は /denpa/logs に残す行数。
const MaxLines = 2000

// Line は記録の 1 行。
type Line struct {
	At   int64  `json:"at"`
	Text string `json:"text"`
}

// Logger は記録。標準出力には `[agent] ` を付けて出す。
type Logger struct {
	mu    sync.Mutex
	out   io.Writer
	lines []Line
}

// New は out に出す記録を作る。out が nil なら出さない。
func New(out io.Writer) *Logger { return &Logger{out: out} }

// Printf は 1 行出す。
func (l *Logger) Printf(format string, args ...any) {
	text := strings.TrimRight(fmt.Sprintf(format, args...), "\r\n")
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, Line{At: time.Now().UnixMilli(), Text: text})
	if len(l.lines) > MaxLines {
		l.lines = append([]Line(nil), l.lines[len(l.lines)-MaxLines:]...)
	}
	if l.out != nil {
		fmt.Fprintf(l.out, "[agent] %s\n", text)
	}
}

// Console は標準出力だけに出す (組むための URL。/denpa/logs には載せない)。
func (l *Logger) Console(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.out != nil {
		fmt.Fprintf(l.out, "[agent] "+format+"\n", args...)
	}
}

// Lines は古い順に返す。
func (l *Logger) Lines() []Line {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Line{}, l.lines...)
}
