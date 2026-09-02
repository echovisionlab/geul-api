// Package mcp implements Geul's MCP 2025-11-25 Streamable HTTP boundary.
//
// Public Geul Personal Access Token authentication terminates at Oathkeeper.
// The handler accepts only an already authenticated Principal in its request
// context and rejects any residual Authorization header.
//
// The PAT owner supplies the credential ID and user-facing credential name
// used for security audit and presence attribution. initialize.clientInfo is
// untrusted protocol metadata only; it never determines the Member, credential,
// actor, or display attribution.
//
// The handler uses the protocol's stateless JSON response mode. It does not
// create MCP transport sessions or server-initiated SSE streams, so GET is
// answered with Method Not Allowed as permitted by the Streamable HTTP
// specification. Domain authorization and behavior remain behind the injected
// tool registry and dispatcher.
package mcp
