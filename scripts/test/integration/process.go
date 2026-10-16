package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/echovisionlab/geul-api/internal/testutil"
)

func runSuiteCommand(ctx context.Context, repoRoot, goWork, minioImage string, arguments []string) error {
	command := exec.CommandContext(ctx, "go", arguments...)
	command.Dir = repoRoot
	command.Env = environmentWithMinIOImage(integrationCommandEnvironment(goWork), minioImage)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := runCommandInProcessGroup(command, 2*time.Second); err != nil {
		return fmt.Errorf("go %s: %w", strings.Join(arguments, " "), err)
	}
	return nil
}

func environmentWithMinIOImage(base []string, image string) []string {
	key := testutil.AppIntegrationMinIOImageEnv + "="
	environment := make([]string, 0, len(base)+1)
	for _, entry := range base {
		if strings.HasPrefix(entry, key) {
			continue
		}
		environment = append(environment, entry)
	}
	return append(environment, key+image)
}

func configureSuiteMinIOImage(image string) error {
	return os.Setenv(testutil.AppIntegrationMinIOImageEnv, image)
}

func runCommandInProcessGroup(command *exec.Cmd, terminationGrace time.Duration) error {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return nil
		}
		err := syscall.Kill(-command.Process.Pid, syscall.SIGTERM)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	command.WaitDelay = 10 * time.Second
	if err := command.Start(); err != nil {
		return err
	}
	processGroupID := command.Process.Pid
	commandErr := command.Wait()
	cleanupErr := terminateProcessGroup(processGroupID, terminationGrace)
	return errors.Join(commandErr, cleanupErr)
}

func terminateProcessGroup(processGroupID int, grace time.Duration) error {
	if processGroupID <= 0 {
		return nil
	}
	if err := signalProcessGroup(processGroupID, syscall.SIGTERM); err != nil {
		return err
	}
	deadline := time.NewTimer(grace)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		exists, err := processGroupExists(processGroupID)
		if err != nil {
			return err
		}
		if !exists {
			return nil
		}
		select {
		case <-deadline.C:
			return signalProcessGroup(processGroupID, syscall.SIGKILL)
		case <-ticker.C:
		}
	}
}

func signalProcessGroup(processGroupID int, signal syscall.Signal) error {
	err := syscall.Kill(-processGroupID, signal)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func processGroupExists(processGroupID int) (bool, error) {
	err := syscall.Kill(-processGroupID, 0)
	switch {
	case err == nil, errors.Is(err, syscall.EPERM):
		return true, nil
	case errors.Is(err, syscall.ESRCH):
		return false, nil
	default:
		return false, err
	}
}

func integrationCommandEnvironment(goWork string) []string {
	return environmentWithGoWork(os.Environ(), goWork)
}

func environmentWithGoWork(base []string, goWork string) []string {
	environment := make([]string, 0, len(base)+1)
	for _, entry := range base {
		if strings.HasPrefix(entry, "GOWORK=") {
			continue
		}
		environment = append(environment, entry)
	}
	return append(environment, "GOWORK="+goWork)
}
