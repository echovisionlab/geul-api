package main

import (
	"fmt"
	"net/http"
	"time"

	"github.com/echovisionlab/geul-api/internal/authentication"
	"github.com/echovisionlab/geul-api/internal/config"
)

type mcpPrivateHandlers struct {
	authorAdmission http.Handler
}

func newMCPPrivateHTTPServer(handlers mcpPrivateHandlers, cfg *config.Config) (*http.Server, error) {
	if handlers.authorAdmission == nil || cfg == nil {
		return nil, fmt.Errorf("MCP private handlers are required")
	}
	mux := http.NewServeMux()
	mux.Handle("POST "+authentication.MCPGatewayAuthorAdmissionPath, handlers.authorAdmission)
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	return &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.MCPPrivatePort),
		Handler:      mux,
		Protocols:    protocols,
		ReadTimeout:  time.Duration(cfg.HTTPReadTimeoutSec) * time.Second,
		WriteTimeout: time.Duration(cfg.HTTPWriteTimeoutSec) * time.Second,
		IdleTimeout:  time.Duration(cfg.HTTPIdleTimeoutSec) * time.Second,
	}, nil
}
