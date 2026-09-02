package architecture

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/echovisionlab/geul-api/internal/auth"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

type externalRequestPolicyEnforcementPoint interface {
	Can(context.Context, policyv1.AuthorizationDecision) (bool, error)
}

type internalActorCapabilityChecker interface {
	CheckActorCan(context.Context, policyv1.Actor, policyv1.Can) (bool, error)
}

type globalSubjectEnumerator interface {
	LookupGlobalSubjects(context.Context, policyv1.SubjectLookup) ([]auth.AccountIdentitySubject, error)
}

func TestAuthorizationPEPExposesSeparateTypedRequestAndActorCapabilitySeams(t *testing.T) {
	var client *auth.SpiceDBClient
	var _ externalRequestPolicyEnforcementPoint = client
	var _ internalActorCapabilityChecker = client
	var _ globalSubjectEnumerator = client
}

func TestProductionAuthorizationWritesUseGeneratedDescriptors(t *testing.T) {
	repositoryRoot := findRepositoryRoot(t)
	authRoot := filepath.Join(repositoryRoot, "internal/auth") + string(filepath.Separator)
	guardPath := filepath.Join(repositoryRoot, "internal/architecture/authorization_pep_test.go")
	for _, root := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(repositoryRoot, root), func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || path == guardPath || strings.HasPrefix(path, authRoot) {
				return nil
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, symbol := range []string{
				"Touch" + "AccountIdentityRelation(",
				"Delete" + "AccountIdentityRelation(",
				"Touch" + "ResourceRelation(",
				"Delete" + "ResourceRelation(",
				"Touch" + "RoleMembership(",
				"Delete" + "RoleMembership(",
				"Touch" + "PlatformRole(",
				"Delete" + "PlatformRole(",
				"Write" + "ResourcePolicy(",
				"Snapshot" + "ResourceRelationships(",
				"Delete" + "ResourceRelationships(",
				"Write" + "RelationshipsExpecting(",
				"Restore" + "RelationshipsExpecting(",
			} {
				if strings.Contains(string(content), symbol) {
					t.Errorf("production authorization write uses raw seam %s in %s", strings.TrimSuffix(symbol, "("), path)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("inspect authorization write root %s: %v", root, err)
		}
	}
}

func TestExternalAdmissionPackagesCannotUseActorCapabilityCheck(t *testing.T) {
	repositoryRoot := findRepositoryRoot(t)
	for _, root := range []string{
		"cmd/server",
		"internal/adapters/mcp",
		"internal/authentication",
	} {
		err := filepath.WalkDir(filepath.Join(repositoryRoot, root), func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if strings.Contains(string(content), ".CheckActorCan(") {
				t.Errorf("external admission package %s uses CheckActorCan; request admission requires AuthorizationDecision plus Can", path)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("inspect external admission root %s: %v", root, err)
		}
	}
}

func TestLegacyRawAuthorizationCheckSeamsAreAbsent(t *testing.T) {
	repositoryRoot := findRepositoryRoot(t)
	guardPath := filepath.Join(repositoryRoot, "internal/architecture/authorization_pep_test.go")
	for _, root := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(repositoryRoot, root), func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || path == guardPath {
				return nil
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, symbol := range []string{
				"Check" + "ResourcePermission(",
				"Check" + "GlobalPermission(",
				"Must" + "BeAdmin(",
			} {
				if strings.Contains(string(content), symbol) {
					t.Errorf("legacy raw authorization seam %s remains in %s", strings.TrimSuffix(symbol, "("), path)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("inspect authorization root %s: %v", root, err)
		}
	}
}
