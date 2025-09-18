// /internal/log/logger.go
package log

import (
	"log"
	"os"
	"strings"
)

type LogLevel int

const (
	levelInfo LogLevel = iota
	levelWarn
	levelError
	levelQuiet
)

var (
	infoLogger   *log.Logger
	warnLogger   *log.Logger
	errorLogger  *log.Logger
	promptLogger *log.Logger
)

type Logger struct {
	level LogLevel
}

var Log *Logger

func Init(levelStr string) {
	infoLogger = log.New(os.Stdout, "[INFO] ", 0)
	warnLogger = log.New(os.Stdout, "[WARN] ", 0)
	errorLogger = log.New(os.Stderr, "[ERROR] ", 0)
	promptLogger = log.New(os.Stdout, "", 0)

	Log = &Logger{}
	Log.setLevelFromString(levelStr)
}

func (l *Logger) setLevelFromString(levelStr string) {
	switch strings.ToLower(levelStr) {
	case "info":
		l.level = levelInfo
	case "warn":
		l.level = levelWarn
	case "error":
		l.level = levelError
	default:
		l.level = levelQuiet
	}
}

func (l *Logger) Info(format string, v ...interface{}) {
	if l.level <= levelInfo {
		infoLogger.Printf(format, v...)
	}
}

func (l *Logger) Warn(format string, v ...interface{}) {
	if l.level <= levelWarn {
		warnLogger.Printf(format, v...)
	}
}

func (l *Logger) Error(format string, v ...interface{}) {
	if l.level <= levelError {
		errorLogger.Printf(format, v...)
	}
}

func (l *Logger) Fatal(format string, v ...interface{}) {
	errorLogger.Printf(format, v...)
	os.Exit(1)
}

func (l *Logger) Prompt(format string, v ...interface{}) {
	promptLogger.Printf(format, v...)
}
