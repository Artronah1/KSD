// Copyright (c) 2026 Artronah1
// SPDX-License-Identifier: MulanPubL-2.0

package main

// log.go — minimal leveled logger.
//
// Design note: ksd writes to stderr on purpose. On OpenWrt, procd captures the
// service's stdout/stderr into syslog, so `logread -e ksd` just works without
// linking against syslog or depending on /dev/log being up (it is not, at
// START=15). An optional file sink is provided for people who want a
// self-contained audit trail.

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

func (l Level) String() string {
	switch l {
	case LevelDebug:
		return "debug"
	case LevelInfo:
		return "info"
	case LevelWarn:
		return "warn"
	case LevelError:
		return "error"
	}
	return "?"
}

type Logger struct {
	mu    sync.Mutex
	min   Level
	out   io.Writer
	file  string
	maxKB int64
}

var L = &Logger{min: LevelInfo, out: os.Stderr}

// SetFile enables an additional sink. maxKB is the rotation threshold; the old
// file is moved to <file>.old (one generation only — this is a router, not a
// log server).
func (l *Logger) SetFile(path string, maxKB int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.file = path
	l.maxKB = maxKB
	if path != "" {
		_ = os.MkdirAll(filepath.Dir(path), 0o700)
	}
}

func (l *Logger) SetLevel(min Level) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.min = min
}

func (l *Logger) Logf(level Level, format string, args ...interface{}) {
	if level < l.min {
		return
	}
	ts := time.Now().Format("2006-01-02 15:04:05")
	line := fmt.Sprintf("%s [%-5s] %s\n", ts, level, fmt.Sprintf(format, args...))

	l.mu.Lock()
	defer l.mu.Unlock()

	_, _ = io.WriteString(l.out, line)

	if l.file == "" {
		return
	}
	if l.maxKB > 0 {
		if fi, err := os.Stat(l.file); err == nil && fi.Size() >= l.maxKB*1024 {
			_ = os.Rename(l.file, l.file+".old")
		}
	}
	f, err := os.OpenFile(l.file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = f.WriteString(line)
	_ = f.Close()
}

func Debugf(f string, a ...interface{}) { L.Logf(LevelDebug, f, a...) }
func Infof(f string, a ...interface{})  { L.Logf(LevelInfo, f, a...) }
func Warnf(f string, a ...interface{})  { L.Logf(LevelWarn, f, a...) }
func Errorf(f string, a ...interface{}) { L.Logf(LevelError, f, a...) }
