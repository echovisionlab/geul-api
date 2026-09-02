package mcp

import (
	"net/http"

	"github.com/echovisionlab/geul-api/internal/auth"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
)

// HTTPConfig assembles the public Oathkeeper-asserted MCP upstream. It has no
// PAT verifier or Member database dependency by design.
type HTTPConfig struct {
	InternalServiceSecret     string
	AuthHeaderName            string
	InternalServiceHeaderName string
	Registry                  mcpserver.ToolRegistry
	Dispatcher                mcpserver.ToolDispatcher
	ServerInfo                mcpserver.Implementation
	ServerTitleSource         mcpserver.ServerTitleSource
	Instructions              string
	AllowedOrigins            []string
	MaxBodyBytes              int64
}

// NewHTTPHandler composes the trusted gateway assertion, normal authenticated
// domain context, and stateless MCP protocol handler.
func NewHTTPHandler(config HTTPConfig) (http.Handler, error) {
	if err := auth.ValidateHeaderNames(config.AuthHeaderName, config.InternalServiceHeaderName); err != nil {
		return nil, err
	}
	registry, err := WrapRegistry(config.Registry)
	if err != nil {
		return nil, err
	}
	dispatcher, err := WrapDispatcher(config.Dispatcher)
	if err != nil {
		return nil, err
	}
	protocol, err := mcpserver.NewHandler(mcpserver.Config{
		Registry: registry, Dispatcher: dispatcher, ServerInfo: config.ServerInfo,
		ServerTitleSource: config.ServerTitleSource,
		Instructions:      config.Instructions,
		AllowedOrigins:    config.AllowedOrigins, MaxBodyBytes: config.MaxBodyBytes,
	})
	if err != nil {
		return nil, err
	}
	return NewGatewayAssertionHandler(
		config.InternalServiceSecret,
		config.AuthHeaderName,
		config.InternalServiceHeaderName,
		protocol,
	)
}
