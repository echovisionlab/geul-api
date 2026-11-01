package referencecatalog_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func clientOwner(id string) referencecatalog.AssetOwner {
	return referencecatalog.AssetOwner{Type: "client", ID: id}
}

func logoAudit(clientID string, slot sharedtelemetry.AuditAssetSlot, op sharedtelemetry.AuditCollectionOperation, fileID string) sharedtelemetry.AuditRecord {
	record := audited(sharedtelemetry.AuditClientUpdated, "client", clientID, "logo")
	record.AssetSlot, record.CollectionOperation, record.FileID = slot, op, fileID
	return record
}

func TestClientLifecycleAuditsEachChangeAndWritesThePolicy(t *testing.T) {
	f, p := newFixture(t, nil, nil), newPrincipal(t)
	website := "https://client.example.test"

	created, err := f.service.CreateClient(p.ctx, referencecatalog.CreateClientCommand{Name: " Client lifecycle ", Website: &website})
	require.NoError(t, err)
	require.Equal(t, "Client lifecycle", created.Client.Name)
	require.Equal(t, website, *created.Client.Website)
	require.Nil(t, created.LightLogo)
	id := created.Client.ID

	name, nextWebsite := "Client lifecycle updated", "https://updated.example.test"
	updated, err := f.service.UpdateClient(p.ctx, referencecatalog.UpdateClientCommand{ID: id, Name: &name, Website: &nextWebsite})
	require.NoError(t, err)
	require.Equal(t, name, updated.Client.Name)
	require.Equal(t, nextWebsite, *updated.Client.Website)
	unchanged, err := f.service.UpdateClient(p.ctx, referencecatalog.UpdateClientCommand{ID: id, Name: &name, Website: &nextWebsite})
	require.NoError(t, err)
	require.Equal(t, updated.Client, unchanged.Client)

	require.NoError(t, f.service.DeleteClient(p.ctx, referencecatalog.DeleteClientCommand{ID: id}))
	_, stored := f.store.StoredClient(id)
	require.False(t, stored)

	f.requireAudited(t, p,
		audited(sharedtelemetry.AuditClientCreated, "client", id),
		audited(sharedtelemetry.AuditClientUpdated, "client", id, "name", "website"),
		audited(sharedtelemetry.AuditClientDeleted, "client", id),
	)
	require.Equal(t, [][]policyv1.RelationshipMutation{
		relationship(t, policyv1.Client.TouchPolicy, id), relationship(t, policyv1.Client.DeletePolicy, id),
	}, f.checker.Applied())
}

func TestClientWritesValidateTheNameAndAnswerATakenOne(t *testing.T) {
	f, p := newFixture(t, nil, nil), newPrincipal(t)
	f.store.SeedClient(referencecatalog.Client{Name: "Taken"})
	other := f.store.SeedClient(referencecatalog.Client{Name: "Other"})

	_, err := f.service.CreateClient(p.ctx, referencecatalog.CreateClientCommand{Name: "  "})
	require.Equal(t, connect.CodeInvalidArgument, codeOf(err))
	require.Empty(t, f.units.Steps(), "a blank name is refused before the transaction")
	blank := " "
	_, err = f.service.UpdateClient(p.ctx, referencecatalog.UpdateClientCommand{ID: other.ID, Name: &blank})
	require.Equal(t, connect.CodeInvalidArgument, codeOf(err))

	_, err = f.service.CreateClient(p.ctx, referencecatalog.CreateClientCommand{Name: "Taken"})
	require.Equal(t, connect.CodeAlreadyExists, codeOf(err))
	require.Equal(t, "client with this name already exists", messageOf(err))
	taken := "Taken"
	_, err = f.service.UpdateClient(p.ctx, referencecatalog.UpdateClientCommand{ID: other.ID, Name: &taken})
	require.Equal(t, connect.CodeAlreadyExists, codeOf(err))
	require.ErrorIs(t, err, referencecatalog.ErrClientNameTaken)
	require.Empty(t, f.audit.Records())
}

func TestClientMutationsAnswerAMissingClientAndRequireAdministrators(t *testing.T) {
	f, p := newFixture(t, &authztest.Checker{}, nil), newPrincipal(t)
	missing := uuid.NewString()
	name := "Missing"

	_, err := f.service.UpdateClient(p.ctx, referencecatalog.UpdateClientCommand{ID: missing, Name: &name})
	require.Equal(t, connect.CodeNotFound, codeOf(err))
	require.Equal(t, "client not found: "+missing, messageOf(err))
	require.Equal(t, connect.CodeNotFound, codeOf(f.service.DeleteClient(p.ctx, referencecatalog.DeleteClientCommand{ID: missing})))
	_, err = f.service.SetClientLogo(p.ctx, referencecatalog.SetClientLogoCommand{ClientID: missing, Slot: referencecatalog.ClientLogoLight, FileID: uuid.NewString()})
	require.Equal(t, connect.CodeNotFound, codeOf(err))
	require.Empty(t, f.checker.Checks(), "a missing root answers before authorization")

	client := f.store.SeedClient(referencecatalog.Client{Name: "Client"})
	_, err = f.service.CreateClient(p.ctx, referencecatalog.CreateClientCommand{Name: "Denied"})
	require.Equal(t, connect.CodePermissionDenied, codeOf(err))
	require.Equal(t, "admin access required", messageOf(err))
	require.Equal(t, connect.CodePermissionDenied, codeOf(f.service.DeleteClient(p.ctx, referencecatalog.DeleteClientCommand{ID: client.ID})))
	require.Equal(t, connect.CodePermissionDenied, codeOf(f.service.DeleteClientLogo(p.ctx, referencecatalog.DeleteClientLogoCommand{ClientID: client.ID, Slot: referencecatalog.ClientLogoDark})))
	_, err = f.service.ListClientsAdmin(p.ctx, referencecatalog.ListClientsAdminQuery{})
	require.Equal(t, connect.CodePermissionDenied, codeOf(err))

	_, err = f.service.CreateClient(context.Background(), referencecatalog.CreateClientCommand{Name: "Anonymous"})
	require.Equal(t, connect.CodeUnauthenticated, codeOf(err))
}

func TestClientDeleteRefusesAClientInUseAndReleasesItsLogos(t *testing.T) {
	f, p := newFixture(t, nil, nil), newPrincipal(t)
	used := f.store.SeedClient(referencecatalog.Client{Name: "Used"})
	f.store.SeedClientWork(used.ID)
	fileID, _ := f.store.SeedReadyFile("logo")
	logoed := f.store.SeedClient(referencecatalog.Client{Name: "Logoed"})
	for _, slot := range []referencecatalog.ClientLogoSlot{referencecatalog.ClientLogoLight, referencecatalog.ClientLogoDark} {
		_, err := f.service.SetClientLogo(p.ctx, referencecatalog.SetClientLogoCommand{ClientID: logoed.ID, Slot: slot, FileID: fileID})
		require.NoError(t, err)
	}

	err := f.service.DeleteClient(p.ctx, referencecatalog.DeleteClientCommand{ID: used.ID})
	require.Equal(t, connect.CodeFailedPrecondition, codeOf(err))
	require.Equal(t, "client is still referenced", messageOf(err))
	_, kept := f.store.StoredClient(used.ID)
	require.True(t, kept)

	require.NoError(t, f.service.DeleteClient(p.ctx, referencecatalog.DeleteClientCommand{ID: logoed.ID}))
	require.Empty(t, f.store.Bindings(clientOwner(logoed.ID)), "every logo binding is released")
}

func TestClientLogoSlotsBindAuditAndReleaseTheirOwnFile(t *testing.T) {
	f, p := newFixture(t, nil, nil), newPrincipal(t)
	client := f.store.SeedClient(referencecatalog.Client{Name: "Logos"})
	lightID, lightAsset := f.store.SeedReadyFile("logo")
	darkID, darkAsset := f.store.SeedReadyFile("logo")

	bound, err := f.service.SetClientLogo(p.ctx, referencecatalog.SetClientLogoCommand{ClientID: client.ID, Slot: referencecatalog.ClientLogoLight, FileID: lightID})
	require.NoError(t, err)
	require.Equal(t, lightAsset, bound)
	again, err := f.service.SetClientLogo(p.ctx, referencecatalog.SetClientLogoCommand{ClientID: client.ID, Slot: referencecatalog.ClientLogoLight, FileID: lightID})
	require.NoError(t, err)
	require.Equal(t, lightAsset, again, "the unchanged selection answers its current asset")
	bound, err = f.service.SetClientLogo(p.ctx, referencecatalog.SetClientLogoCommand{ClientID: client.ID, Slot: referencecatalog.ClientLogoDark, FileID: darkID})
	require.NoError(t, err)
	require.Equal(t, darkAsset, bound)
	require.Equal(t, map[string]string{"logo_light": lightID, "logo_dark": darkID}, f.store.Bindings(clientOwner(client.ID)))

	view, err := f.service.GetClient(p.ctx, referencecatalog.GetClientQuery{ID: client.ID})
	require.NoError(t, err)
	require.Equal(t, lightAsset, view.LightLogo)
	require.Equal(t, darkAsset, view.DarkLogo)

	require.NoError(t, f.service.DeleteClientLogo(p.ctx, referencecatalog.DeleteClientLogoCommand{ClientID: client.ID, Slot: referencecatalog.ClientLogoDark}))
	require.NoError(t, f.service.DeleteClientLogo(p.ctx, referencecatalog.DeleteClientLogoCommand{ClientID: client.ID, Slot: referencecatalog.ClientLogoDark}))
	require.Equal(t, map[string]string{"logo_light": lightID}, f.store.Bindings(clientOwner(client.ID)), "a slot releases only its own binding")
	stored, _ := f.store.StoredClient(client.ID)
	require.Equal(t, lightID, *stored.LogoLightFileID)
	require.Nil(t, stored.LogoDarkFileID)

	f.requireAudited(t, p,
		logoAudit(client.ID, sharedtelemetry.AuditAssetSlotLight, sharedtelemetry.AuditCollectionOperationAdded, lightID),
		logoAudit(client.ID, sharedtelemetry.AuditAssetSlotDark, sharedtelemetry.AuditCollectionOperationAdded, darkID),
		logoAudit(client.ID, sharedtelemetry.AuditAssetSlotDark, sharedtelemetry.AuditCollectionOperationRemoved, darkID),
	)
}

func TestClientLogoRejectsAnUnsupportedSlotAndAnUnattachableFile(t *testing.T) {
	f, p := newFixture(t, nil, nil), newPrincipal(t)
	client := f.store.SeedClient(referencecatalog.Client{Name: "Logos"})

	_, err := f.service.SetClientLogo(p.ctx, referencecatalog.SetClientLogoCommand{ClientID: client.ID, Slot: "", FileID: uuid.NewString()})
	require.Equal(t, connect.CodeInvalidArgument, codeOf(err))
	require.Equal(t, "variant: unsupported theme asset variant", messageOf(err))
	require.Equal(t, connect.CodeInvalidArgument, codeOf(f.service.DeleteClientLogo(p.ctx, referencecatalog.DeleteClientLogoCommand{ClientID: client.ID, Slot: "sepia"})))
	require.Empty(t, f.units.Steps())

	_, err = f.service.SetClientLogo(p.ctx, referencecatalog.SetClientLogoCommand{ClientID: client.ID, Slot: referencecatalog.ClientLogoLight, FileID: uuid.NewString()})
	require.Equal(t, connect.CodeFailedPrecondition, codeOf(err))
	stored, _ := f.store.StoredClient(client.ID)
	require.Nil(t, stored.LogoLightFileID)
}

func TestClientLogoChangeRollsBackWhenAuditFails(t *testing.T) {
	auditDown := errors.New("audit unavailable")
	f, p := newFixture(t, nil, &audittest.Recorder{Err: auditDown}), newPrincipal(t)
	client := f.store.SeedClient(referencecatalog.Client{Name: "Rollback"})
	fileID, _ := f.store.SeedReadyFile("logo")

	_, err := f.service.SetClientLogo(p.ctx, referencecatalog.SetClientLogoCommand{ClientID: client.ID, Slot: referencecatalog.ClientLogoLight, FileID: fileID})
	require.ErrorIs(t, err, auditDown)
	stored, _ := f.store.StoredClient(client.ID)
	require.Nil(t, stored.LogoLightFileID)
	require.Empty(t, f.store.Bindings(clientOwner(client.ID)))

	require.ErrorIs(t, f.service.DeleteClient(p.ctx, referencecatalog.DeleteClientCommand{ID: client.ID}), auditDown)
	_, kept := f.store.StoredClient(client.ID)
	require.True(t, kept)
	require.Equal(t, mutation.StepRollback, f.lastStep())
}

func TestClientReadsResolveLogosAndServeWithoutAnUnresolvableOne(t *testing.T) {
	f, p := newFixture(t, nil, nil), newPrincipal(t)
	lightID, lightAsset := f.store.SeedReadyFile("logo")
	unready := uuid.NewString()
	blank := " "
	alpha := f.store.SeedClient(referencecatalog.Client{Name: "Alpha", LogoLightFileID: &lightID, LogoDarkFileID: &unready})
	beta := f.store.SeedClient(referencecatalog.Client{Name: "Beta", LogoLightFileID: &blank})
	f.store.SeedClientWork(beta.ID)
	var logs bytes.Buffer
	ctx := p.ctx
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	view, err := f.service.GetClient(ctx, referencecatalog.GetClientQuery{ID: alpha.ID})
	require.NoError(t, err)
	require.Equal(t, referencecatalog.ClientView{Client: alpha, LightLogo: lightAsset}, view)
	require.Contains(t, logs.String(), "Client logo asset lookup failed; serving the client without this logo")

	page, err := f.service.ListClients(ctx, referencecatalog.ListClientsQuery{})
	require.NoError(t, err)
	require.Equal(t, []referencecatalog.ClientView{{Client: alpha, LightLogo: lightAsset}, {Client: beta}}, page.Items)
	require.Equal(t, referencecatalog.Pagination{Total: 2, Limit: 20}, page.Pagination)

	found, err := f.service.SearchClients(ctx, referencecatalog.SearchClientsQuery{Query: "BET", Limit: 99})
	require.NoError(t, err)
	require.Equal(t, []referencecatalog.ClientView{{Client: beta}}, found)

	admin, err := f.service.ListClientsAdmin(ctx, referencecatalog.ListClientsAdminQuery{Limit: 1, Offset: 1})
	require.NoError(t, err)
	require.Equal(t, []referencecatalog.ClientWorkCount{{Client: referencecatalog.ClientView{Client: beta}, WorkCount: 1}}, admin.Items)

	_, err = f.service.GetClient(ctx, referencecatalog.GetClientQuery{ID: uuid.NewString()})
	require.Equal(t, connect.CodeNotFound, codeOf(err))
}

func TestClientSearchLimitsToTenOutsideItsRange(t *testing.T) {
	f := newFixture(t, nil, nil)
	for range 12 {
		f.store.SeedClient(referencecatalog.Client{Name: "Client " + uuid.NewString()})
	}
	for _, limit := range []int32{0, -1, 51} {
		found, err := f.service.SearchClients(context.Background(), referencecatalog.SearchClientsQuery{Limit: limit})
		require.NoError(t, err)
		require.Len(t, found, 10, limit)
	}
	found, err := f.service.SearchClients(context.Background(), referencecatalog.SearchClientsQuery{Limit: 50})
	require.NoError(t, err)
	require.Len(t, found, 12)
}

func TestUpdateClientServesWithoutLogosItCannotRead(t *testing.T) {
	f, p := newFixture(t, nil, nil), newPrincipal(t)
	client := f.store.SeedClient(referencecatalog.Client{Name: "Client"})
	f.store.Fail("ClientLogoFiles", errors.New("logo lookup down"))
	name := "Renamed"

	updated, err := f.service.UpdateClient(p.ctx, referencecatalog.UpdateClientCommand{ID: client.ID, Name: &name})

	require.NoError(t, err)
	require.Equal(t, name, updated.Client.Name)
	require.Nil(t, updated.LightLogo)
	require.Nil(t, updated.DarkLogo)
}
