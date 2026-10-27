package runner

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// OpLog is an operation's log file. Writes are split into lines; each line has every registered
// secret replaced with *** before it hits disk or the onLine callback (SSE).
type OpLog struct {
	mu      sync.Mutex
	f       *os.File
	secrets []string
	buf     []byte
	onLine  func(string)
	tail    []string // last lines, for error messages
}

func LogPath(dataDir, opID string) string { return filepath.Join(dataDir, "logs", opID+".log") }

func CreateLog(dataDir, opID string, onLine func(string)) (*OpLog, error) {
	f, err := os.OpenFile(LogPath(dataDir, opID), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o640)
	if err != nil {
		return nil, err
	}
	return &OpLog{f: f, onLine: onLine}, nil
}

// AddSecret registers a value to redact. Must be called before the secret can appear in output.
func (l *OpLog) AddSecret(s string) {
	if s == "" {
		return
	}
	l.mu.Lock()
	l.secrets = append(l.secrets, s)
	l.mu.Unlock()
}

func (l *OpLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf = append(l.buf, p...)
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			break
		}
		l.emit(string(l.buf[:i]))
		l.buf = l.buf[i+1:]
	}
	return len(p), nil
}

func (l *OpLog) emit(line string) {
	line = strings.TrimRight(line, "\r")
	for _, s := range l.secrets {
		line = strings.ReplaceAll(line, s, "***")
	}
	fmt.Fprintf(l.f, "%s %s\n", time.Now().UTC().Format("15:04:05"), line)
	if l.tail = append(l.tail, line); len(l.tail) > 5 {
		l.tail = l.tail[1:]
	}
	if l.onLine != nil {
		l.onLine(line)
	}
}

func (l *OpLog) Printf(format string, args ...any) { fmt.Fprintf(l, format+"\n", args...) }

// Tail returns the last few (redacted) lines joined, for operation messages.
func (l *OpLog) Tail() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.tail, " | ")
}

// Redact applies the registered secrets to an arbitrary string (e.g. error messages).
func (l *OpLog) Redact(s string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, sec := range l.secrets {
		s = strings.ReplaceAll(s, sec, "***")
	}
	return s
}

func (l *OpLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.buf) > 0 {
		l.emit(string(l.buf))
		l.buf = nil
	}
	return l.f.Close()
}
