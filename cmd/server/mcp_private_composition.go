package main

import (
	"fmt"
	"net/http"
	"time"

	"github.com/echovisionlab/geul-api/internal/adapters/patauth"
	"github.com/echovisionlab/geul-api/internal/authentication"
	"github.com/echovisionlab/geul-api/internal/config"
)

type mcpPrivateHandlers struct {
	authorAdmission http.Handler
	oathkeeperPAT   http.Handler
}

// newMCPPrivateHTTPServer also hosts Oathkeeper's PAT session check. Both
// internal gateway callbacks stay off the main application listener.
func newMCPPrivateHTTPServer(handlers mcpPrivateHandlers, cfg *config.Config) (*http.Server, error) {
	if handlers.authorAdmission == nil || handlers.oathkeeperPAT == nil || cfg == nil {
		return nil, fmt.Errorf("private gateway handlers are required")
	}
	mux := http.NewServeMux()
	mux.Handle("POST "+authentication.MCPGatewayAuthorAdmissionPath, handlers.authorAdmission)
	mux.Handle(patauth.OathkeeperPath, handlers.oathkeeperPAT)
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	return &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.MCPPrivatePort),
		Handler:      recoverHTTPPanics(mux),
		Protocols:    protocols,
		ReadTimeout:  time.Duration(cfg.HTTPReadTimeoutSec) * time.Second,
		WriteTimeout: time.Duration(cfg.HTTPWriteTimeoutSec) * time.Second,
		IdleTimeout:  time.Duration(cfg.HTTPIdleTimeoutSec) * time.Second,
	}, nil
}
