package logger

import (
	"fmt"
	"os"
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

type Logger struct {
	level  Level
	prefix string
}

func New(level Level, prefix string) *Logger {
	return &Logger{
		level:  level,
		prefix: prefix,
	}
}

func (l *Logger) log(level Level, levelStr string, format string, args ...interface{}) {
	if level < l.level {
		return
	}

	timestamp := time.Now().Format("15:04:05")
	message := fmt.Sprintf(format, args...)
	
	var prefixColor *color.Color
	var levelColor *color.Color
	
	switch level {
	case DEBUG:
		levelColor = color.New(color.FgHiBlack)
		prefixColor = color.New(color.FgHiBlack)
	case INFO:
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
	
	fmt.Fprintf(os.Stdout, "%s %s%s %s\n",
		color.New(color.FgHiBlack).Sprintf("[%s]", timestamp),
		prefix,
		levelColor.Sprintf("%-5s", levelStr),
		message,
	)
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

