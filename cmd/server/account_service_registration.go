package main

import (
	memberpat "github.com/echovisionlab/geul-api/internal/member/pat"

	"crypto/rand"
	"fmt"
	"io"
	"net/http"

	"github.com/echovisionlab/geul-api/internal/account"
	accountpostgres "github.com/echovisionlab/geul-api/internal/account/postgres"
	accountadapter "github.com/echovisionlab/geul-api/internal/adapters/account"
	authenticationadapter "github.com/echovisionlab/geul-api/internal/adapters/authentication"
	memberadapter "github.com/echovisionlab/geul-api/internal/adapters/member"
	memberpatadapter "github.com/echovisionlab/geul-api/internal/adapters/memberpat"
	"github.com/echovisionlab/geul-api/internal/adapters/patauth"
	"github.com/echovisionlab/geul-api/internal/authentication"
	"github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/member"
	memberpostgres "github.com/echovisionlab/geul-api/internal/member/postgres"
	mutation "github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
)

type registeredAccounts struct {
	units           account.Units
	account         *account.Service
	emailProjection memberadapter.AccountEmailProjection
	memberUnits     mutation.UnitOfWork[member.Store]
	members         *member.Service
}

func (r serviceRegistration) registerAccounts(fileService *filemedia.Service) (registeredAccounts, error) {
	deps := r.dependencies
	accountUnits, err := accountpostgres.NewUnits(deps.db, accountpostgres.Ports{
		Members: accountadapter.MemberDeletion{}, MemberEmails: accountadapter.MemberEmailProjection{},
		Newsletter: accountadapter.NewsletterSubscription{}, Publisher: deps.workerPublisher,
	})
	if err != nil {
		return registeredAccounts{}, fmt.Errorf("initialize account units: %w", err)
	}
	accounts, err := account.NewService(account.Deps{
		Units:      accountUnits,
		SpiceDB:    deps.spicedbClient,
		Fence:      identitystate.PostgresFence{},
		Identity:   deps.kratosClient,
		Emails:     accountpostgres.EmailCommands{Publisher: deps.workerPublisher},
		Members:    accountpostgres.MemberAuthorizations(deps.db, accountadapter.MemberDeletion{}, deps.spicedbClient),
		Audit:      deps.telemetryWriter,
		SiteOrigin: deps.cfg.SiteOrigin,
	})
	if err != nil {
		return registeredAccounts{}, fmt.Errorf("initialize account service: %w", err)
	}
	accountEmailProjection := memberadapter.AccountEmailProjection{Units: accountUnits, Identity: deps.kratosClient}
	memberUnits, err := memberpostgres.NewUnits(deps.db, memberpostgres.Ports{
		WelcomeEmails: deps.servicePublisher, WelcomeRecipients: accountEmailProjection, CDNDomain: deps.cfg.CDNURL,
	})
	if err != nil {
		return registeredAccounts{}, fmt.Errorf("initialize member units: %w", err)
	}
	memberService, err := member.NewService(member.Deps{
		Units:       memberUnits,
		SpiceDB:     deps.spicedbClient,
		Fence:       identitystate.PostgresFence{},
		Identity:    deps.kratosClient,
		AvatarFiles: memberadapter.AvatarFiles{Files: fileService},
		Accounts:    memberadapter.Accounts{Accounts: accounts},
		Audit:       deps.telemetryWriter,
		SiteOrigin:  deps.cfg.SiteOrigin,
	})
	if err != nil {
		return registeredAccounts{}, fmt.Errorf("initialize member service: %w", err)
	}
	return registeredAccounts{units: accountUnits, account: accounts, emailProjection: accountEmailProjection, memberUnits: memberUnits, members: memberService}, nil
}

type registeredAccountHooks struct {
	oathkeeper   *patauth.OathkeeperHandler
	account      *accountadapter.PersonalAccessTokenHandler
	mcpAdmission http.Handler
	emailChanges *account.AccountEmailChangeLifecycle
}

func (r serviceRegistration) registerAccountHooks(composition registeredAccounts) (registeredAccountHooks, error) {
	deps := r.dependencies
	personalAccessTokenRepository, err := memberpatadapter.NewPersonalAccessTokenRepository(deps.db)
	if err != nil {
		return registeredAccountHooks{}, fmt.Errorf("initialize personal access token repository: %w", err)
	}
	personalAccessTokenHandlers, err := newPersonalAccessTokenComposition(
		account.NewConnectHandler(composition.account),
		personalAccessTokenRepository,
		memberpatadapter.SystemClock{},
		rand.Reader,
	)
	if err != nil {
		return registeredAccountHooks{}, err
	}
	if err := registerPATVerification(deps.mux, deps.cfg.PATVerificationSecret, personalAccessTokenHandlers.tokens); err != nil { //nolint:staticcheck // legacy Worker verifier credential, retained for migration
		return registeredAccountHooks{}, fmt.Errorf("initialize PAT verification: %w", err)
	}
	oathkeeperPATHandler, err := patauth.NewOathkeeper(deps.cfg.TokenSigningSecret, deps.cfg.InternalServiceHeaderName, personalAccessTokenHandlers.tokens)
	if err != nil {
		return registeredAccountHooks{}, fmt.Errorf("initialize Oathkeeper PAT verification: %w", err)
	}
	accountService := personalAccessTokenHandlers.accountHandler
	mcpAuthorAdmissionHandler, err := authentication.NewMCPGatewayAuthorAdmissionHandler(
		deps.cfg.TokenSigningSecret,
		deps.cfg.AuthHeaderName,
		deps.cfg.InternalServiceHeaderName,
		deps.spicedbClient,
	)
	if err != nil {
		return registeredAccountHooks{}, fmt.Errorf("initialize MCP gateway author admission: %w", err)
	}

	accountEmailChangeLifecycle := account.NewAccountEmailChangeLifecycle(
		composition.units,
		deps.kratosClient,
		accountpostgres.EmailCommands{Publisher: deps.hooksPublisher},
	)
	directRoles := accountadapter.AccountDirectRoleTransition{}
	registrationMembers := member.NewMemberProvisioner(
		composition.memberUnits,
		deps.kratosClient,
		deps.spicedbClient,
		composition.emailProjection,
		directRoles,
	)
	loginHooks := authentication.NewLoginHookService(
		deps.kratosClient,
		authenticationadapter.NewLoginMemberProvisioner(registrationMembers),
		r.authenticationService,
	)
	registrationHooks := authentication.NewRegistrationHookPolicy(
		authenticationadapter.NewRegistrationReuseHoldChecker(deps.db),
	)
	accountSettingsHooks := account.NewAccountSettingsHookService(deps.kratosClient, accountEmailChangeLifecycle)
	credentialHooks := account.NewAccountCredentialHookLifecycle(
		composition.units,
		deps.kratosClient,
		deps.telemetryWriter,
		accountpostgres.EmailCommands{Publisher: deps.hooksPublisher},
	)
	// Hooks handler (for Kratos webhooks)
	authenticationHooks := authenticationadapter.NewHooksHandler(loginHooks, registrationHooks)
	accountHooks := accountadapter.NewSettingsHooksHandler(accountSettingsHooks, credentialHooks)
	protectInternalHook := func(h http.HandlerFunc) http.Handler {
		return r.internalTrust.identity(h)
	}
	authenticationHooks.RegisterRoutes(deps.mux, protectInternalHook)
	accountHooks.RegisterRoutes(deps.mux, protectInternalHook)

	return registeredAccountHooks{oathkeeper: oathkeeperPATHandler, account: accountService, mcpAdmission: mcpAuthorAdmissionHandler, emailChanges: accountEmailChangeLifecycle}, nil
}

type personalAccessTokenComposition struct {
	tokens         *memberpat.Service
	accountHandler *accountadapter.PersonalAccessTokenHandler
}

func newPersonalAccessTokenComposition(
	delegate managev1connect.AccountServiceHandler,
	repository memberpat.Repository,
	clock memberpat.Clock,
	random io.Reader,
) (personalAccessTokenComposition, error) {
	tokens, err := memberpat.NewService(repository, clock, random)
	if err != nil {
		return personalAccessTokenComposition{}, fmt.Errorf("initialize personal access token service: %w", err)
	}
	accountHandler, err := accountadapter.NewPersonalAccessTokenHandler(delegate, tokens)
	if err != nil {
		return personalAccessTokenComposition{}, fmt.Errorf("initialize Account personal access token handler: %w", err)
	}
	return personalAccessTokenComposition{
		tokens: tokens, accountHandler: accountHandler,
	}, nil
}
