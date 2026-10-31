//go:build integration

package menu

import "testing"

func newTestHandler(t *testing.T, deps Deps) *ConnectHandler {
	t.Helper()
	return NewConnectHandler(newTestService(t, deps))
}
