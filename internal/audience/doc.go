// Package audience owns audience segment domain behavior and its Connect
// handler. Its use cases depend on ports: the Store (audience/postgres in
// production, audiencetest in unit tests), authorization, the identity
// fence, audit and the recipient counter.
package audience
