package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMCPAuthorAdmissionHasOneOwnerAndMainHandlerDoesNotRecheck(t *testing.T) {
	repositoryRoot := findRepositoryRoot(t)
	ownerSource := readArchitectureSource(t, repositoryRoot, "internal/authentication/mcp_gateway_admission.go")
	if count := strings.Count(ownerSource, "policyv1.Platform.IsAuthor()"); count != 1 {
		t.Fatalf("MCP admission owner has %d Platform.IsAuthor constructors, want exactly one", count)
	}

	for _, path := range []string{
		"internal/adapters/mcp/gateway_authentication.go",
		"internal/adapters/mcp/http.go",
		"cmd/server/ai_document_mcp_composition.go",
	} {
		source := readArchitectureSource(t, repositoryRoot, path)
		for _, forbidden := range []string{"Platform.IsAuthor", "AuthorizationDecisionChecker"} {
			if strings.Contains(source, forbidden) {
				t.Fatalf("%s retains duplicate MCP admission dependency %q", path, forbidden)
			}
		}
	}
}

func readArchitectureSource(t *testing.T, repositoryRoot, path string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(repositoryRoot, path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(content)
}
