package application

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"gorm.io/gorm"
)

type translationInterchangeAuthorizationDomains struct {
	testTranslationDomains
	viewErr   error
	editErr   error
	viewCalls int
	editCalls int
}

func (domains *translationInterchangeAuthorizationDomains) RequireTranslationInterchangeView(
	context.Context,
	*gorm.DB,
	*auth.SpiceDBClient,
	string,
	string,
) error {
	domains.viewCalls++
	return domains.viewErr
}

func (domains *translationInterchangeAuthorizationDomains) RequireTranslationInterchangeEdit(
	context.Context,
	*gorm.DB,
	*auth.SpiceDBClient,
	string,
	string,
) error {
	domains.editCalls++
	return domains.editErr
}

func TestTranslationInterchangeArchivedAuthorCanExportButCannotImport(t *testing.T) {
	domains := &translationInterchangeAuthorizationDomains{
		editErr: errs.NoPermission("edit_archived", "legal policy"),
	}
	service := &TranslationService{domains: domains}

	if err := service.requireTranslationInterchangeView(
		context.Background(), nil, "terms", "policy-a",
	); err != nil {
		t.Fatalf("archived Author export preflight failed: %v", err)
	}
	if domains.viewCalls != 1 || domains.editCalls != 0 {
		t.Fatalf("export preflight calls = view %d, edit %d; want view only", domains.viewCalls, domains.editCalls)
	}
	if err := service.requireTranslationInterchangeEdit(
		context.Background(), nil, "terms", "policy-a",
	); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("archived Author import preflight error = %v, want permission denied", err)
	}
	if domains.viewCalls != 1 || domains.editCalls != 1 {
		t.Fatalf("preflight calls = view %d, edit %d; want one each", domains.viewCalls, domains.editCalls)
	}
}

func TestTranslationInterchangeAdminCanExportAndImport(t *testing.T) {
	domains := &translationInterchangeAuthorizationDomains{}
	service := &TranslationService{domains: domains}

	if err := service.requireTranslationInterchangeView(
		context.Background(), nil, "terms", "policy-a",
	); err != nil {
		t.Fatalf("Admin export preflight failed: %v", err)
	}
	if domains.viewCalls != 1 || domains.editCalls != 0 {
		t.Fatalf("export preflight calls = view %d, edit %d; want view only", domains.viewCalls, domains.editCalls)
	}
	if err := service.requireTranslationInterchangeEdit(
		context.Background(), nil, "terms", "policy-a",
	); err != nil {
		t.Fatalf("Admin import preflight failed: %v", err)
	}
	if domains.viewCalls != 1 || domains.editCalls != 1 {
		t.Fatalf("preflight calls = view %d, edit %d; want one each", domains.viewCalls, domains.editCalls)
	}
}

func TestTranslationInterchangeOrdinaryAuthorCanExportAndImport(t *testing.T) {
	domains := &translationInterchangeAuthorizationDomains{}
	service := &TranslationService{domains: domains}

	if err := service.requireTranslationInterchangeView(
		context.Background(), nil, "post", "post-a",
	); err != nil {
		t.Fatalf("ordinary Author export preflight failed: %v", err)
	}
	if domains.viewCalls != 1 || domains.editCalls != 0 {
		t.Fatalf("export preflight calls = view %d, edit %d; want view only", domains.viewCalls, domains.editCalls)
	}
	if err := service.requireTranslationInterchangeEdit(
		context.Background(), nil, "post", "post-a",
	); err != nil {
		t.Fatalf("ordinary Author import preflight failed: %v", err)
	}
	if domains.viewCalls != 1 || domains.editCalls != 1 {
		t.Fatalf("preflight calls = view %d, edit %d; want one each", domains.viewCalls, domains.editCalls)
	}
}
