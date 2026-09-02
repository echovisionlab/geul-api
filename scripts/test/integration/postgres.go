package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/lib/pq"
)

type suitePostgres struct {
	ContainerID string
	AdminDSN    string
	Template    string
}

func startSuitePostgres(ctx context.Context, options suiteOptions) (*suitePostgres, error) {
	if err := requireLocalPostgresImage(ctx, options.PostgresImage); err != nil {
		return nil, err
	}
	templateName, err := randomSuiteIdentifier("geul_it_template_")
	if err != nil {
		return nil, err
	}
	image := options.PostgresImage

	arguments := suitePostgresDockerArguments(options, templateName)
	output, err := dockerOutput(ctx, arguments...)
	if err != nil {
		return nil, fmt.Errorf("start suite PostgreSQL container from local image %s: %w", image, err)
	}
	containerID := strings.TrimSpace(output)
	suite := &suitePostgres{ContainerID: containerID, Template: templateName}
	failed := true
	defer func() {
		if failed {
			_ = suite.Close()
		}
	}()

	waitCtx, cancelWait := context.WithTimeout(ctx, 2*time.Minute)
	port, err := waitForPostgresPort(waitCtx, containerID)
	cancelWait()
	if err != nil {
		return nil, err
	}
	adminURL := &url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword("test", "test"),
		Host:     net.JoinHostPort("127.0.0.1", port),
		Path:     "/postgres",
		RawQuery: "sslmode=disable",
	}
	templateURL := *adminURL
	templateURL.Path = "/" + templateName
	suite.AdminDSN = adminURL.String()
	if err := prepareSuitePostgresTemplate(ctx, templateURL.String(), options.SchemaRoot); err != nil {
		return nil, fmt.Errorf("prepare suite PostgreSQL template: %w", err)
	}
	failed = false
	return suite, nil
}

func suitePostgresDockerArguments(options suiteOptions, templateName string) []string {
	arguments := []string{
		"run", "--pull=never", "--rm", "--detach",
		"--label", "geul.integration.owner=suite-orchestrator",
		"--env", "POSTGRES_DB=" + templateName,
		"--env", "POSTGRES_USER=test",
		"--env", "POSTGRES_PASSWORD=test",
		"--publish", "127.0.0.1::5432",
		"--tmpfs", "/var/lib/postgresql:rw",
	}
	return append(arguments, options.PostgresImage)
}

func requireLocalPostgresImage(ctx context.Context, image string) error {
	return requireLocalDockerImages(ctx, []string{image})
}

func prepareSuitePostgresTemplate(ctx context.Context, dsn, schemaRoot string) (prepareErr error) {
	database, err := sql.Open("postgres", dsn)
	if err != nil {
		return fmt.Errorf("open suite PostgreSQL template: %w", err)
	}
	defer func() {
		prepareErr = errors.Join(prepareErr, database.Close())
	}()

	deadline := time.Now().Add(30 * time.Second)
	for {
		pingContext, cancel := context.WithTimeout(ctx, 2*time.Second)
		err = database.PingContext(pingContext)
		cancel()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("wait for suite PostgreSQL template: %w", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}

	schemaPath := filepath.Join(schemaRoot, "schema.sql")
	schemaSQL, err := os.ReadFile(schemaPath)
	if err != nil {
		return fmt.Errorf("read reviewed schema SQL %s: %w", schemaPath, err)
	}
	if _, err := database.ExecContext(ctx, string(schemaSQL)); err != nil {
		return fmt.Errorf("apply reviewed schema SQL: %w", err)
	}
	var memberRelation sql.NullString
	if err := database.QueryRowContext(ctx, "SELECT to_regclass('public.member')::text").Scan(&memberRelation); err != nil {
		return fmt.Errorf("verify suite PostgreSQL template schema: %w", err)
	}
	if !memberRelation.Valid || memberRelation.String == "" {
		return fmt.Errorf("suite PostgreSQL template is missing public.member")
	}
	var pgmqExtension bool
	if err := database.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pgmq')").Scan(&pgmqExtension); err != nil {
		return fmt.Errorf("verify suite PostgreSQL template extension: %w", err)
	}
	if !pgmqExtension {
		return fmt.Errorf("suite PostgreSQL template is missing pgmq extension")
	}
	return nil
}

func (suite *suitePostgres) Close() error {
	if suite == nil || suite.ContainerID == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := dockerOutput(ctx, "rm", "--force", suite.ContainerID)
	if err != nil && !strings.Contains(err.Error(), "No such container") {
		return fmt.Errorf("remove suite PostgreSQL container: %w", err)
	}
	suite.ContainerID = ""
	return nil
}

func waitForPostgresPort(ctx context.Context, containerID string) (string, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		output, err := dockerOutput(ctx, "port", containerID, "5432/tcp")
		if err == nil {
			address := strings.TrimSpace(output)
			_, port, splitErr := net.SplitHostPort(address)
			if splitErr == nil && port != "" {
				return port, nil
			}
		}
		status, inspectErr := dockerOutput(ctx, "inspect", "--format", "{{.State.Status}}", containerID)
		if inspectErr != nil {
			return "", fmt.Errorf("inspect suite PostgreSQL container while waiting for port: %w", inspectErr)
		}
		if isTerminalPostgresContainerStatus(status) {
			logs, logsErr := dockerOutput(ctx, "logs", "--tail", "100", containerID)
			if logsErr != nil {
				logs = "unavailable: " + logsErr.Error()
			}
			return "", fmt.Errorf(
				"suite PostgreSQL container entered %s before publishing its port; logs: %s",
				strings.TrimSpace(status),
				strings.TrimSpace(logs),
			)
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("wait for suite PostgreSQL port: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func isTerminalPostgresContainerStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case "dead", "exited":
		return true
	default:
		return false
	}
}

func dockerOutput(ctx context.Context, arguments ...string) (string, error) {
	command := exec.CommandContext(ctx, "docker", arguments...)
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker %s: %w: %s", arguments[0], err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func randomSuiteIdentifier(prefix string) (string, error) {
	value := make([]byte, 10)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate suite resource identifier: %w", err)
	}
	return prefix + hex.EncodeToString(value), nil
}
