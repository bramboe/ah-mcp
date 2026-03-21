package tools

import (
	"fmt"
	"io"
	"os"
	"time"
)

var logOutput io.Writer = os.Stderr

// InitLogger configures logging. Always writes to stderr (visible in journalctl).
// If logFile is non-empty, also appends to that file.
func InitLogger(logFile string) {
	if logFile == "" {
		return
	}
	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[AH-MCP] WARNING: cannot open log file %s: %v\n", logFile, err)
		return
	}
	logOutput = io.MultiWriter(os.Stderr, f)
}

func logLine(level, tool, msg string) {
	fmt.Fprintf(logOutput, "[AH-MCP] %s %-5s %-35s %s\n",
		time.Now().UTC().Format("2006-01-02T15:04:05Z"),
		level,
		tool+":",
		msg,
	)
}

// LogInfo logs an informational message for a tool.
func LogInfo(tool, format string, args ...any) {
	logLine("INFO", tool, fmt.Sprintf(format, args...))
}

// LogWarn logs a warning for a tool.
func LogWarn(tool, format string, args ...any) {
	logLine("WARN", tool, fmt.Sprintf(format, args...))
}

// LogError logs an error for a tool.
func LogError(tool, format string, args ...any) {
	logLine("ERROR", tool, fmt.Sprintf(format, args...))
}
