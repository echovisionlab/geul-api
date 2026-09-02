//go:build integration

package testutil

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/testcontainers/testcontainers-go"
)

// prepareOathkeeperIntegrationFiles renders the public Identity repository's
// checked-in Oathkeeper templates into a private temporary directory. The
// deployment renderer owns this operation in production; the API integration
// harness supplies only local, valid origins and boundary names so Oathkeeper
// can validate the same runtime configuration without relying on deployment
// secrets or a second repository process.
func prepareOathkeeperIntegrationFiles() (string, []testcontainers.ContainerFile, error) {
	templateDirectory := appIntegrationRepoPath("../../../../infra/oathkeeper")
	outputDirectory, err := os.MkdirTemp("", "geul-oathkeeper-integration-*")
	if err != nil {
		return "", nil, fmt.Errorf("create Oathkeeper integration config directory: %w", err)
	}

	rendered := map[string]map[string]string{
		"oathkeeper.yml": {
			"__KRATOS_PUBLIC_URL__":            "http://kratos:4433",
			"__HYDRA_ADMIN_URL__":              "http://hydra:4445",
			"__AUTHORIZATION_URL__":            "http://api:8000",
			"__INTERNAL_SERVICE_HEADER_NAME__": "X-Internal-Service",
			"__SESSION_COOKIE_NAME__":          oryTestSessionCookieName,
		},
		"rules.yml": {
			"__AUTH_HEADER_NAME__":             "X-Authenticated-Context-B64",
			"__INTERNAL_SERVICE_HEADER_NAME__": "X-Internal-Service",
		},
	}

	files := make([]testcontainers.ContainerFile, 0, len(rendered))
	for name, replacements := range rendered {
		sourcePath := filepath.Join(templateDirectory, name)
		contents, err := os.ReadFile(sourcePath)
		if err != nil {
			_ = os.RemoveAll(outputDirectory)
			return "", nil, fmt.Errorf("read Oathkeeper integration template %s: %w", sourcePath, err)
		}
		renderedContents := string(contents)
		for placeholder, replacement := range replacements {
			if !strings.Contains(renderedContents, placeholder) {
				_ = os.RemoveAll(outputDirectory)
				return "", nil, fmt.Errorf("Oathkeeper integration template %s is missing placeholder %q", sourcePath, placeholder)
			}
			renderedContents = strings.ReplaceAll(renderedContents, placeholder, replacement)
		}
		for placeholder := range replacements {
			if strings.Contains(renderedContents, placeholder) {
				_ = os.RemoveAll(outputDirectory)
				return "", nil, fmt.Errorf("Oathkeeper integration template %s contains unresolved placeholder %q", sourcePath, placeholder)
			}
		}

		outputPath := filepath.Join(outputDirectory, name)
		if err := os.WriteFile(outputPath, []byte(renderedContents), 0o644); err != nil {
			_ = os.RemoveAll(outputDirectory)
			return "", nil, fmt.Errorf("write Oathkeeper integration config %s: %w", outputPath, err)
		}
		files = append(files, testcontainers.ContainerFile{
			HostFilePath:      outputPath,
			ContainerFilePath: "/etc/oathkeeper/" + name,
			FileMode:          0o644,
		})
	}
	return outputDirectory, files, nil
}
