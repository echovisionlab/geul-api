package main

import (
	"net/http"

	"github.com/echovisionlab/geul-api/internal/adapters/opendiscogsauth"
)

func registerOpenDiscogsAuthentication(mux *http.ServeMux, secret string, tokens opendiscogsauth.Authenticator) error {
	if secret == "" {
		return nil
	}
	handler, err := opendiscogsauth.New(secret, tokens)
	if err != nil {
		return err
	}
	mux.Handle(opendiscogsauth.Path, handler)
	return nil
}
