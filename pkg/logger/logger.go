package logger

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/fatih/color"
)

type Level int

const (
	DEBUG Level = iota
	INFO
	WARN
	ERROR
)

// TUIBackend is an interface for TUI log backends
type TUIBackend interface {
	SendLog(level, prefix, message string)
}

type Logger struct {
	level    Level
	prefix   string
	tuiBackend TUIBackend
	mu       sync.Mutex
	useTUI   bool
}

func New(level Level, prefix string) *Logger {
	return &Logger{
		level:  level,
		prefix: prefix,
		useTUI: false,
	}
}

// SetTUIBackend sets the TUI backend for logging
func (l *Logger) SetTUIBackend(backend TUIBackend) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.tuiBackend = backend
	l.useTUI = backend != nil
}

// SetTUIEnabled enables or disables TUI mode
func (l *Logger) SetTUIEnabled(enabled bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.useTUI = enabled && l.tuiBackend != nil
}

func (l *Logger) log(level Level, levelStr string, format string, args ...interface{}) {
	if level < l.level {
		return
	}

	timestamp := time.Now().Format("15:04:05")
	message := fmt.Sprintf(format, args...)
	
	l.mu.Lock()
	useTUI := l.useTUI
	tuiBackend := l.tuiBackend
	l.mu.Unlock()
	
	// Send to TUI if enabled
	if useTUI && tuiBackend != nil {
		tuiBackend.SendLog(levelStr, l.prefix, message)
	}
	
	// Also output to stdout (for file redirection or non-TUI mode)
	var prefixColor *color.Color
	var levelColor *color.Color
	
	switch level {
	case DEBUG:
		// Subtle gray for technical details
		levelColor = color.New(color.FgHiBlack)
		prefixColor = color.New(color.FgHiBlack)
	case INFO:
		// Normal cyan for execution steps
		levelColor = color.New(color.FgCyan)
		prefixColor = color.New(color.FgWhite)
	case WARN:
		levelColor = color.New(color.FgYellow)
		prefixColor = color.New(color.FgYellow)
	case ERROR:
		levelColor = color.New(color.FgRed)
		prefixColor = color.New(color.FgRed)
	}

	prefix := ""
	if l.prefix != "" {
		prefix = prefixColor.Sprintf("[%s] ", l.prefix)
	}
	
	// Only print to stdout if not in TUI mode (TUI handles its own display)
	if !useTUI {
		fmt.Fprintf(os.Stdout, "%s %s%s %s\n",
			color.New(color.FgHiBlack).Sprintf("[%s]", timestamp),
			prefix,
			levelColor.Sprintf("%-5s", levelStr),
			message,
		)
	}
}

// Tool logs tool execution with distinct formatting
func (l *Logger) Tool(format string, args ...interface{}) {
	if INFO < l.level {
		return
	}
	timestamp := time.Now().Format("15:04:05")
	message := fmt.Sprintf(format, args...)
	
	l.mu.Lock()
	useTUI := l.useTUI
	tuiBackend := l.tuiBackend
	l.mu.Unlock()
	
	// Send to TUI if enabled
	if useTUI && tuiBackend != nil {
		tuiBackend.SendLog("TOOL", l.prefix, message)
	}
	
	// Also output to stdout if not in TUI mode
	if !useTUI {
		prefix := ""
		if l.prefix != "" {
			prefix = color.New(color.FgWhite).Sprintf("[%s] ", l.prefix)
		}
		// Use distinct color for tool execution (bright green)
		fmt.Fprintf(os.Stdout, "%s %s%s %s\n",
			color.New(color.FgHiBlack).Sprintf("[%s]", timestamp),
			prefix,
			color.New(color.FgGreen).Sprintf("TOOL "),
			color.New(color.FgWhite).Sprintf(message),
		)
	}
}

func (l *Logger) Debug(format string, args ...interface{}) {
	l.log(DEBUG, "DEBUG", format, args...)
}

func (l *Logger) Info(format string, args ...interface{}) {
	l.log(INFO, "INFO", format, args...)
}

func (l *Logger) Warn(format string, args ...interface{}) {
	l.log(WARN, "WARN", format, args...)
}

func (l *Logger) Error(format string, args ...interface{}) {
	l.log(ERROR, "ERROR", format, args...)
}

func (l *Logger) WithPrefix(prefix string) *Logger {
	newPrefix := l.prefix
	if newPrefix != "" {
		newPrefix += "." + prefix
	} else {
		newPrefix = prefix
	}
	return New(l.level, newPrefix)
}

