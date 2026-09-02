package main

import (
	"fmt"
	"net/http"
)

func registerMCPRoutes(mux *http.ServeMux, handler http.Handler) error {
	if mux == nil || handler == nil {
		return fmt.Errorf("MCP route mux and handler are required")
	}
	mux.Handle("/mcp", http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet && request.Method != http.MethodPost {
			response.Header().Set("Allow", http.MethodGet+", "+http.MethodPost)
			http.Error(response, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		handler.ServeHTTP(response, request)
	}))
	return nil
}
