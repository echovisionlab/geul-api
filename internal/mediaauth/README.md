# Media authorization contract

Imported unchanged from `echovisionlab/geul-mediaauth` v0.1.0 at
`baf92640deb5e37db2f920cfb15a31bb1b8af6fd`. Both `token.go` and
`token_test.go` are byte-for-byte copies of the version previously used by API.
The original license is retained here.

This internal package owns canonical delivery/object paths, HMAC token issuance
and validation, token lifetimes and request scope checks. Moving the package
changes no wire format, signing secret, URL, expiry, or authorization behavior.
All API consumers, including the imported media services, use this package.

Run `GOWORK=off go test -race -cover ./internal/mediaauth`. The original golden
V1 token test and full statement-coverage requirement are retained in API CI.
