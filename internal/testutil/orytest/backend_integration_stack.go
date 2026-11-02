//go:build integration

package orytest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	pgtest "github.com/echovisionlab/geul-api/internal/testutil/pgtest"
)

func RegisterSuiteHookUpstream(ctx context.Context, backend *pgtest.AppIntegrationBackendLease, rawUpstream string) (func(context.Context) error, error) {
	upstream, err := url.Parse(strings.TrimSpace(rawUpstream))
	if err != nil {
		return nil, fmt.Errorf("parse suite hook upstream: %w", err)
	}
	if upstream.Hostname() == pgtest.IntegrationHostInternal {
		upstream.Host = "127.0.0.1:" + upstream.Port()
	}
	payload, err := json.Marshal(map[string]string{"upstream_base_url": upstream.String()})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, backend.HookControlURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+backend.HookControlToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("register suite hook upstream: %w", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return nil, fmt.Errorf("register suite hook upstream: status %d", response.StatusCode)
	}
	registration := response.Header.Get("Integration-Hook-Registration")
	if strings.TrimSpace(registration) == "" {
		return nil, fmt.Errorf("register suite hook upstream: missing registration")
	}
	return func(ctx context.Context) error {
		request, err := http.NewRequestWithContext(ctx, http.MethodDelete, backend.HookControlURL, nil)
		if err != nil {
			return err
		}
		request.Header.Set("Authorization", "Bearer "+backend.HookControlToken)
		request.Header.Set("Integration-Hook-Registration", registration)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			return err
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusNoContent {
			return fmt.Errorf("unregister suite hook upstream: status %d", response.StatusCode)
		}
		return nil
	}, nil
}
