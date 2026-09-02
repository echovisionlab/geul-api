package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunCommandInProcessGroupCleansDescendantsAfterLeaderExit(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name     string
		exitCode int
		wantErr  bool
	}{
		{name: "success", exitCode: 0},
		{name: "failure", exitCode: 7, wantErr: true},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			pidPath := filepath.Join(t.TempDir(), "descendant.pid")
			script := fmt.Sprintf(
				`(trap '' TERM HUP; while :; do sleep 30; done) & echo $! > %q; exit %d`,
				pidPath,
				testCase.exitCode,
			)
			command := exec.CommandContext(context.Background(), "/bin/sh", "-c", script)
			err := runCommandInProcessGroup(command, 100*time.Millisecond)
			if testCase.wantErr != (err != nil) {
				t.Fatalf("command error = %v, wantErr=%t", err, testCase.wantErr)
			}

			contents, readErr := os.ReadFile(pidPath)
			if readErr != nil {
				t.Fatalf("read descendant PID: %v", readErr)
			}
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(contents)))
			if parseErr != nil {
				t.Fatalf("parse descendant PID: %v", parseErr)
			}
			requireProcessGone(t, pid)
		})
	}
}

func requireProcessGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if err != nil && !errors.Is(err, syscall.EPERM) {
			t.Fatalf("inspect descendant %d: %v", pid, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("descendant process %d remains after process-group cleanup", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
