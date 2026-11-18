//go:build windows

package xexec

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
	"golang.org/x/text/encoding/simplifiedchinese"
)

func TestWindowsCommandAttributesAndUnstartedCancel(t *testing.T) {
	e := &XCmd{ctx: context.Background(), command: "program.exe", args: []string{"hello world", "", "&"}}
	e.initCmd()
	if e.Path != "program.exe" || len(e.Args) != 4 || e.Args[1] != "hello world" || e.Args[2] != "" || e.Args[3] != "&" {
		t.Fatalf("direct executable arguments changed: %#v", e.Args)
	}
	if e.SysProcAttr == nil || !e.SysProcAttr.HideWindow || e.SysProcAttr.CreationFlags&windows.CREATE_NEW_PROCESS_GROUP == 0 || e.SysProcAttr.CmdLine != "" {
		t.Fatalf("unexpected command attributes: %+v", e.SysProcAttr)
	}
	if err := e.Cancel(); err == nil {
		t.Fatal("cancel before Start succeeded")
	}

	path := filepath.Join(t.TempDir(), "exit-code")
	s := &script{cmd: exec.CommandContext(context.Background(), "program.exe"), exitCodePath: path, logger: &captureTestLogger{}}
	s.beforeExec()
	if s.cmd.SysProcAttr == nil || !s.cmd.SysProcAttr.HideWindow || s.cmd.SysProcAttr.CreationFlags&windows.CREATE_NEW_PROCESS_GROUP == 0 {
		t.Fatalf("unexpected script attributes: %+v", s.cmd.SysProcAttr)
	}
	if err := s.cmd.Cancel(); err == nil {
		t.Fatal("script cancel before Start succeeded")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "137" {
		t.Fatalf("cancellation exit code = %q, error = %v", data, err)
	}
}

func TestWindowsGBKDetectionAndConversion(t *testing.T) {
	s := &script{logger: &captureTestLogger{}}
	for _, tc := range []struct {
		name, input string
		want        bool
	}{
		{"empty", "", false},
		{"ascii", "plain text\t123", false},
		{"chinese", "\xd6\xd0\xce\xc4", true},
		{"mixed", "prefix \xd6\xd0 suffix", true},
		{"lower-bound", "\x81\x40", true},
		{"upper-bound", "\xfe\xfe", true},
		{"truncated", "\xd6", false},
		{"invalid-lead", "\x80\x40", false},
		{"invalid-trail", "\x81\x3f", false},
		{"excluded-trail", "\x81\x7f", false},
		{"high-trail", "\x81\xff", false},
		{"invalid-after-valid", "\xd6\xd0\xff", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.isGBK(tc.input); got != tc.want {
				t.Fatalf("isGBK(%q) = %t, want %t", tc.input, got, tc.want)
			}
		})
	}
	encoded, err := simplifiedchinese.GBK.NewEncoder().String("中文 output")
	if err != nil {
		t.Fatal(err)
	}
	if got := s.gbkToUtf8(encoded); got != "中文 output" {
		t.Fatalf("GBK conversion = %q", got)
	}
	// No parallel tests: acp is a package-level cache and must be restored.
	oldACP := acp
	defer func() { acp = oldACP }()
	for _, cp := range []uint32{936, 65001} {
		acp = cp
		if got := s.transform(encoded); got != "中文 output" {
			t.Fatalf("code page %d: conversion = %q", cp, got)
		}
		if got := s.transform("ASCII"); got != "ASCII" {
			t.Fatalf("code page %d: ASCII = %q", cp, got)
		}
	}
	acp = 65001
	if got := s.transform("\x80"); got != "\x80" {
		t.Fatalf("invalid non-GBK input changed: %q", got)
	}
}

func TestWindowsKillAllRejectsInvalidPID(t *testing.T) {
	for _, pid := range []int32{-1 << 31, -2, -1, 0, 4} {
		if err := KillAll(pid); err == nil {
			t.Fatalf("KillAll(%d) succeeded", pid)
		}
	}
}

func TestWindowsReaperDoesNotStart(t *testing.T) {
	r := &childReaper{}
	r.ensureStarted()
	if r.managed != nil || r.changed != nil || r.opaque != 0 {
		t.Fatalf("non-Linux reaper changed state: %+v", r)
	}
}
