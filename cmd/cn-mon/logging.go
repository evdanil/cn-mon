package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type fileLogger struct {
	mu        sync.Mutex
	file      *os.File
	threshold int
}

func openLogger(cfg settings, homeDir string) (*fileLogger, error) {
	if homeDir == "" {
		var err error
		homeDir, err = currentHomeDir()
		if err != nil {
			return nil, err
		}
	}
	path, err := expandHome(cfg.LogFile, homeDir)
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(path) {
		if cwd, err := os.Getwd(); err == nil {
			path = filepath.Join(cwd, path)
		}
	}
	if err := prepareLog(path); err != nil {
		path = filepath.Join(homeDir, "cn.log")
		if err := prepareLog(path); err != nil {
			return nil, fmt.Errorf("prepare log file: %w", err)
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}
	return &fileLogger{file: f, threshold: levelNumber(cfg.LogLevel)}, nil
}

func prepareLog(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	return f.Close()
}

func (l *fileLogger) Log(level, message string) error {
	level = strings.ToUpper(level)
	number := levelNumber(level)
	if number < l.threshold {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return os.ErrClosed
	}
	_, err := fmt.Fprintf(l.file, "%s - %s - %s\n", time.Now().Local().Format("2006-01-02 15:04:05,000"), level, message)
	return err
}

func (l *fileLogger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

func normalizeLevel(s string) string {
	switch strings.ToUpper(s) {
	case "DEBUG", "INFO", "WARNING", "ERROR", "CRITICAL", "NOTSET":
		return strings.ToUpper(s)
	case "WARN":
		return "WARNING"
	case "FATAL":
		return "CRITICAL"
	default:
		return "INFO"
	}
}
func levelNumber(s string) int {
	switch normalizeLevel(s) {
	case "NOTSET":
		return 0
	case "DEBUG":
		return 10
	case "INFO":
		return 20
	case "WARNING":
		return 30
	case "ERROR":
		return 40
	case "CRITICAL":
		return 50
	default:
		return 20
	}
}
