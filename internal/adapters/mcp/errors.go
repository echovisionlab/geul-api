package mcp

import "errors"

var (
	ErrInvalidPrincipal  = errors.New("mcp authentication principal is invalid")
	ErrInvalidDependency = errors.New("mcp authentication dependency is invalid")
)
