package main

import (
	"net/http"

	"github.com/echovisionlab/geul-api/internal/adapters/patauth"
)

func registerPATVerification(mux *http.ServeMux, secret string, tokens patauth.Authenticator) error {
	if secret == "" {
		return nil
	}
	handler, err := patauth.New(secret, tokens)
	if err != nil {
		return err
	}
	mux.Handle(patauth.Path, handler)
	return nil
}
