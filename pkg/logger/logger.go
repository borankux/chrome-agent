package logger

import (
	"fmt"
	"os"
	"path/filepath"
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
	level      Level
	prefix     string
	tuiBackend TUIBackend
	mu         sync.Mutex
	useTUI     bool
	logFile    *os.File
	logFilePath string
	logFileMu  sync.Mutex
}

func New(level Level, prefix string) *Logger {
	l := &Logger{
		level:  level,
		prefix: prefix,
		useTUI: false,
	}
	l.initLogFile()
	return l
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

// initLogFile initializes the log file with date prefix and number suffix
func (l *Logger) initLogFile() {
	l.logFileMu.Lock()
	defer l.logFileMu.Unlock()

	// Create logs directory if it doesn't exist
	logsDir := "logs"
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		// If we can't create the directory, log to stderr and continue without file logging
		fmt.Fprintf(os.Stderr, "Warning: Failed to create logs directory: %v\n", err)
		return
	}

	// Generate log filename with date prefix
	now := time.Now()
	datePrefix := now.Format("2006-01-02")
	
	// Find next available number for today's date
	logNum := 1
	for {
		filename := fmt.Sprintf("%s-%d.log", datePrefix, logNum)
		logPath := filepath.Join(logsDir, filename)
		
		// Check if file exists
		if _, err := os.Stat(logPath); os.IsNotExist(err) {
			// File doesn't exist, use this number
			l.logFilePath = logPath
			break
		}
		logNum++
	}

	// Open/create log file
	var err error
	l.logFile, err = os.OpenFile(l.logFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		// If we can't open the file, log to stderr and continue without file logging
		fmt.Fprintf(os.Stderr, "Warning: Failed to open log file %s: %v\n", l.logFilePath, err)
		l.logFile = nil
		return
	}
}

// writeToFile writes a log entry to the file
func (l *Logger) writeToFile(levelStr, message string) {
	l.logFileMu.Lock()
	defer l.logFileMu.Unlock()

	if l.logFile == nil {
		return
	}

	// Format: YYYY-MM-DD HH:MM:SS LEVEL [prefix] message
	now := time.Now()
	timestamp := now.Format("2006-01-02 15:04:05")
	
	prefix := ""
	if l.prefix != "" {
		prefix = fmt.Sprintf("[%s] ", l.prefix)
	}
	
	// Pad level to 5 characters to match stdout format
	paddedLevel := fmt.Sprintf("%-5s", levelStr)
	logLine := fmt.Sprintf("%s %s%s %s\n", timestamp, paddedLevel, prefix, message)
	
	// Write to file (ignore errors to avoid disrupting normal operation)
	_, _ = l.logFile.WriteString(logLine)
}

// Close closes the log file
func (l *Logger) Close() error {
	l.logFileMu.Lock()
	defer l.logFileMu.Unlock()

	if l.logFile != nil {
		err := l.logFile.Close()
		l.logFile = nil
		return err
	}
	return nil
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
	
	// Write to file (in addition to stdout/TUI)
	l.writeToFile(levelStr, message)
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
	
	// Write to file (in addition to stdout/TUI)
	l.writeToFile("TOOL", message)
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
	// Create new logger without initializing a new log file
	// Share the same log file with the parent logger
	newLogger := &Logger{
		level:       l.level,
		prefix:      newPrefix,
		useTUI:      l.useTUI,
		tuiBackend:  l.tuiBackend,
		logFile:     l.logFile,
		logFilePath: l.logFilePath,
	}
	return newLogger
}

