//go:build integration

package artistlabelpublic

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"

	artistadapter "github.com/echovisionlab/geul-api/internal/adapters/artist"
	labeladapter "github.com/echovisionlab/geul-api/internal/adapters/label"
	releaseadapter "github.com/echovisionlab/geul-api/internal/adapters/release"
	sharelinkadapter "github.com/echovisionlab/geul-api/internal/adapters/sharelink"
	sitesettingsadapter "github.com/echovisionlab/geul-api/internal/adapters/sitesettings"
	workadapter "github.com/echovisionlab/geul-api/internal/adapters/work"
	artistdomain "github.com/echovisionlab/geul-api/internal/artist"
	artistpublic "github.com/echovisionlab/geul-api/internal/artist/public"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	labeldomain "github.com/echovisionlab/geul-api/internal/label"
	labelpublic "github.com/echovisionlab/geul-api/internal/label/public"
	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/og"
	releasedomain "github.com/echovisionlab/geul-api/internal/release"
	sharelinkdomain "github.com/echovisionlab/geul-api/internal/sharelink"
	"github.com/echovisionlab/geul-api/internal/testutil"
	workdomain "github.com/echovisionlab/geul-api/internal/work"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

const artistLabelPublicCDN = "https://cdn.example.com"

// ArtistLabelPublicFixture contains only the cross-domain records consumed by
// the Artist and Label public read integration tests. Assertions stay in each
// owning public package.
type ArtistLabelPublicFixture struct {
	DB               *gorm.DB
	SpiceDB          *auth.SpiceDBClient
	AdminID          string
	Context          context.Context
	Suffix           string
	ArtistService    *artistpublic.ArtistService
	ArtistMedia      artistpublic.MediaProjection
	LabelService     *labelpublic.LabelService
	ShareLinkService *sharelinkdomain.Service

	ParentLabel          *managev1.Label
	MainLabel            *managev1.Label
	DraftLabel           *managev1.Label
	DarkOnlyLabel        *managev1.Label
	BareReleaseLabel     *managev1.Label
	MainLabelDescription string
	LabelLightLogoID     string
	LabelDarkLogoID      string
	DarkOnlyLogoID       string
	MainLabelOGAssetID   string
	MainLabelOGAssetURL  string

	MainArtist           *managev1.Artist
	OtherArtist          *managev1.Artist
	BareReleaseArtist    *managev1.Artist
	DraftArtist          *managev1.Artist
	MainArtistBio        string
	ArtistImageFileID    string
	MainArtistOGAssetID  string
	MainArtistOGAssetURL string

	Release              *managev1.Release
	BareReleaseID        string
	ReleaseArtworkFileID string
	Work                 *managev1.Work
	BareWorkID           string
	WorkSummary          string
	WorkImageFileID      string
}

func New(t *testing.T) *ArtistLabelPublicFixture {
	t.Helper()

	stack, err := testutil.StartBackendIntegrationStack(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stack.Close()) })
	db := stack.Postgres.DB
	spiceDB := stack.SpiceDBClient

	adminID := uuid.NewString()
	testutil.SeedPostIntegrationIdentity(t, db, adminID, "Public Artist Label Admin")
	testutil.GrantPostIntegrationRole(t, spiceDB, adminID, policyv1.Role.Admin())
	ctx := testutil.PostIntegrationContext(adminID)
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	identityManager := testutil.NewPostIdentityManager(testutil.PostIntegrationIdentity(adminID, "en"))
	contentBlocks := testutil.NewCreativeContentStore(t, spiceDB)
	publisher := artistLabelAsyncPublisher{}
	fileDeleter := artistLabelFileDeleter{}
	artistRuntime := artistadapter.NewRuntime(artistLabelPublicCDN, artistFixtureOGRefresher(db))

	manageArtist := artistdomain.NewArtistService(
		db, spiceDB, identityManager,
		fileDeleter, publisher,
		artistdomain.Dependencies{
			Translation: artistadapter.NewTranslation(),
			Members:     artistadapter.NewMemberProjection(db, artistLabelPublicCDN),
			Runtime:     artistRuntime,
		},
		artistdomain.WithArtistContentBlockStore(contentBlocks),
	)
	manageLabel := labeldomain.NewLabelService(
		db, spiceDB, identityManager,
		fileDeleter, publisher,
		labeldomain.Dependencies{
			Translation: labeladapter.NewTranslation(),
			Members:     labeladapter.NewMemberProjection(db, artistLabelPublicCDN),
			Runtime:     labeladapter.NewRuntime(artistLabelPublicCDN, labelFixtureOGRefresher(db)),
		},
		labeldomain.WithLabelContentBlockStore(contentBlocks),
	)
	manageRelease := releasedomain.NewReleaseService(
		db, spiceDB, identityManager, releaseadapter.NewTrackFiles(fileDeleter),
		releaseadapter.NewAssets(artistLabelPublicCDN), releaseadapter.NewOG(artistLabelPublicCDN),
		releaseadapter.NewWaveformJobs(db, artistLabelTranscoderPublisher{}), publisher,
		releasedomain.WithReleaseContentBlockStore(contentBlocks),
	)
	manageWork := workdomain.NewWorkService(
		db, workFixtureRuntime(db), spiceDB, identityManager, publisher,
		workdomain.WithWorkContentBlockStore(contentBlocks),
		workdomain.WithWorkContentBlockMediaHydrator(artistLabelMediaHydrator{}),
		workdomain.WithWorkMemberSummaryLoader(workadapter.NewMemberSummaries(db, artistLabelPublicCDN)),
	)
	shareLinks := sharelinkdomain.NewService(db, sharelinkadapter.NewAuthority(db, spiceDB, nil))
	artistMedia := artistadapter.NewPublicMedia(db, artistRuntime)
	publicArtist := artistpublic.NewArtistService(
		db, spiceDB, artistMedia,
		artistpublic.WithArtistContentBlockStore(contentBlocks),
	)
	labelRuntime := labeladapter.NewPublicRuntime(db, artistLabelPublicCDN, spiceDB, contentBlocks)
	publicLabel := labelpublic.NewLabelService(
		db,
		labelpublic.Dependencies{Content: labelRuntime, Draft: labelRuntime, Media: labelRuntime},
	)

	parentLabel := createPublishedArtistLabelFixtureLabel(
		t, manageLabel, ctx, "Public Parent Label "+suffix, "public-parent-label-"+suffix, nil,
	)
	mainLabelDescription := "Public Main Label " + suffix + " updated public description"
	mainLabel := createPublishedArtistLabelFixtureLabel(
		t, manageLabel, ctx, "Public Main Label "+suffix, "public-main-label-"+suffix,
		&parentLabel.Id, mainLabelDescription,
	)
	labelLightLogoID := seedArtistLabelFixtureFile(t, db, "public-label/"+suffix+"/logo-light.png", "image/png", "logo")
	labelDarkLogoID := seedArtistLabelFixtureFile(t, db, "public-label/"+suffix+"/logo-dark.webp", "image/webp", "logo")
	_, err = manageLabel.SetLabelImage(ctx, connect.NewRequest(&managev1.SetLabelImageRequest{
		LabelId: mainLabel.Id, FileId: labelLightLogoID,
		Variant: managev1.ThemeAssetVariant_THEME_ASSET_VARIANT_LIGHT,
	}))
	require.NoError(t, err)
	_, err = manageLabel.SetLabelImage(ctx, connect.NewRequest(&managev1.SetLabelImageRequest{
		LabelId: mainLabel.Id, FileId: labelDarkLogoID,
		Variant: managev1.ThemeAssetVariant_THEME_ASSET_VARIANT_DARK,
	}))
	require.NoError(t, err)
	mainLabelOGAssetID, mainLabelOGAssetURL := seedArtistLabelFixtureOGAsset(t, db, "label", mainLabel.Id)
	require.NoError(t, db.Model(&model.Label{}).Where("id = ?", mainLabel.Id).Update("og_asset_id", mainLabelOGAssetID).Error)
	draftLabel := createDraftArtistLabelFixtureLabel(
		t, manageLabel, ctx, "Public Main Label Draft "+suffix, "public-main-label-draft-"+suffix, nil,
	)
	darkOnlyLabel := createPublishedArtistLabelFixtureLabel(
		t, manageLabel, ctx, "Public Dark Only Label "+suffix, "public-dark-only-label-"+suffix, nil,
	)
	darkOnlyLogoID := seedArtistLabelFixtureFile(t, db, "public-label/"+suffix+"/dark-only.webp", "image/webp", "logo")
	_, err = manageLabel.SetLabelImage(ctx, connect.NewRequest(&managev1.SetLabelImageRequest{
		LabelId: darkOnlyLabel.Id, FileId: darkOnlyLogoID,
		Variant: managev1.ThemeAssetVariant_THEME_ASSET_VARIANT_DARK,
	}))
	require.NoError(t, err)

	mainArtistBio := "Public Main Artist " + suffix + " updated public bio"
	mainArtist := createPublishedArtistLabelFixtureArtist(
		t, manageArtist, ctx, "Public Main Artist "+suffix, "public-main-artist-"+suffix, mainArtistBio,
	)
	otherArtist := createPublishedArtistLabelFixtureArtist(
		t, manageArtist, ctx, "Public Other Artist "+suffix, "public-other-artist-"+suffix,
	)
	bareReleaseArtist := createPublishedArtistLabelFixtureArtist(
		t, manageArtist, ctx, "Public Bare Release Artist "+suffix, "public-bare-release-artist-"+suffix,
	)
	bareReleaseLabel := createPublishedArtistLabelFixtureLabel(
		t, manageLabel, ctx, "Public Bare Release Label "+suffix, "public-bare-release-label-"+suffix, nil,
	)
	draftArtist := createDraftArtistLabelFixtureArtist(
		t, manageArtist, ctx, "Public Main Artist Draft "+suffix, "public-main-artist-draft-"+suffix,
	)
	childArtist, err := manageArtist.CreateArtist(ctx, connect.NewRequest(&managev1.CreateArtistRequest{
		Name: "Public Child Artist " + suffix, Slug: stringPointer("public-child-artist-" + suffix),
		ParentArtistId: &mainArtist.Id, Document: testutil.CreativeContentDocument("en", "Public Child Artist "+suffix),
	}))
	require.NoError(t, err)
	_, err = manageArtist.PublishArtist(ctx, connect.NewRequest(&managev1.PublishArtistRequest{Id: childArtist.Msg.Id}))
	require.NoError(t, err)
	linkArtistLabelFixture(t, db, spiceDB, contentBlocks, publisher, ctx, mainArtist.Id, mainLabel.Id)
	artistImageFileID := seedArtistLabelFixtureFile(t, db, "artist-image.png", "image/png", "image")
	artistImage, err := manageArtist.SetArtistImage(ctx, connect.NewRequest(&managev1.SetArtistImageRequest{
		ArtistId: mainArtist.Id, FileId: artistImageFileID,
	}))
	require.NoError(t, err)
	require.Equal(t, AssetURL(t, db, artistImageFileID), artistImage.Msg.GetImageAsset().GetUrl())
	mainArtistOGAssetID, mainArtistOGAssetURL := seedArtistLabelFixtureOGAsset(t, db, "artist", mainArtist.Id)
	require.NoError(t, db.Table("artist_translation").
		Where("entity_id = ? AND locale = ?", mainArtist.Id, "en").
		Update("og_asset_id", mainArtistOGAssetID).Error)

	releaseDate := time.Now().UTC().AddDate(0, -2, 0)
	releaseResponse, err := manageRelease.CreateRelease(ctx, connect.NewRequest(&managev1.CreateReleaseRequest{
		Title: "Public Artist Label Release " + suffix, Slug: stringPointer("public-artist-label-release-" + suffix),
		Type: managev1.ReleaseType_RELEASE_TYPE_ALBUM, ReleaseDate: timestamppb.New(releaseDate),
		Document: testutil.CreativeContentDocument("en", "Public Artist Label Release "+suffix),
	}))
	require.NoError(t, err)
	_, err = manageRelease.SetReleaseArtists(ctx, connect.NewRequest(&managev1.SetReleaseArtistsRequest{
		ReleaseId: releaseResponse.Msg.Id,
		Artists:   []*managev1.ReleaseArtistInput{{ArtistId: mainArtist.Id, SortOrder: 1}, {ArtistId: otherArtist.Id, SortOrder: 2}},
	}))
	require.NoError(t, err)
	_, err = manageRelease.SetReleaseLabels(ctx, connect.NewRequest(&managev1.SetReleaseLabelsRequest{
		ReleaseId: releaseResponse.Msg.Id,
		Labels:    []*managev1.ReleaseLabelInput{{LabelId: mainLabel.Id, SortOrder: 1}},
	}))
	require.NoError(t, err)
	releaseArtworkFileID := seedArtistLabelFixtureFile(t, db, "release-artwork.png", "image/png", "artwork")
	releaseArtwork, err := manageRelease.SetReleaseArtwork(ctx, connect.NewRequest(&managev1.SetReleaseArtworkRequest{
		ReleaseId: releaseResponse.Msg.Id, FileId: releaseArtworkFileID,
	}))
	require.NoError(t, err)
	require.Equal(t, AssetURL(t, db, releaseArtworkFileID), releaseArtwork.Msg.GetArtworkAsset().GetUrl())
	publishedRelease, err := manageRelease.PublishRelease(ctx, connect.NewRequest(&managev1.PublishReleaseRequest{Id: releaseResponse.Msg.Id}))
	require.NoError(t, err)
	require.Equal(t, managev1.ReleaseStatus_RELEASE_STATUS_PUBLISHED, publishedRelease.Msg.Status)

	bareReleaseResponse, err := manageRelease.CreateRelease(ctx, connect.NewRequest(&managev1.CreateReleaseRequest{
		Title: "Public Artist Label Bare Release " + suffix,
		Slug:  stringPointer("public-artist-label-bare-release-" + suffix),
		Type:  managev1.ReleaseType_RELEASE_TYPE_SINGLE, ReleaseDate: timestamppb.New(releaseDate.AddDate(0, 1, 0)),
		Document: testutil.CreativeContentDocument("en", "Public Artist Label Bare Release "+suffix),
	}))
	require.NoError(t, err)
	_, err = manageRelease.SetReleaseArtists(ctx, connect.NewRequest(&managev1.SetReleaseArtistsRequest{
		ReleaseId: bareReleaseResponse.Msg.Id,
		Artists:   []*managev1.ReleaseArtistInput{{ArtistId: bareReleaseArtist.Id, SortOrder: 1}},
	}))
	require.NoError(t, err)
	_, err = manageRelease.SetReleaseLabels(ctx, connect.NewRequest(&managev1.SetReleaseLabelsRequest{
		ReleaseId: bareReleaseResponse.Msg.Id,
		Labels:    []*managev1.ReleaseLabelInput{{LabelId: bareReleaseLabel.Id, SortOrder: 1}},
	}))
	require.NoError(t, err)
	bareRelease, err := manageRelease.PublishRelease(ctx, connect.NewRequest(&managev1.PublishReleaseRequest{Id: bareReleaseResponse.Msg.Id}))
	require.NoError(t, err)

	draftRelease, err := manageRelease.CreateRelease(ctx, connect.NewRequest(&managev1.CreateReleaseRequest{
		Title:    "Public Artist Label Draft Release " + suffix,
		Type:     managev1.ReleaseType_RELEASE_TYPE_SINGLE,
		Document: testutil.CreativeContentDocument("en", "Public Artist Label Draft Release "+suffix),
	}))
	require.NoError(t, err)
	_, err = manageRelease.SetReleaseArtists(ctx, connect.NewRequest(&managev1.SetReleaseArtistsRequest{
		ReleaseId: draftRelease.Msg.Id,
		Artists:   []*managev1.ReleaseArtistInput{{ArtistId: mainArtist.Id, SortOrder: 1}},
	}))
	require.NoError(t, err)
	_, err = manageRelease.SetReleaseLabels(ctx, connect.NewRequest(&managev1.SetReleaseLabelsRequest{
		ReleaseId: draftRelease.Msg.Id,
		Labels:    []*managev1.ReleaseLabelInput{{LabelId: mainLabel.Id, SortOrder: 1}},
	}))
	require.NoError(t, err)

	isPresent := true
	workSummary := "Public artist label work summary"
	workResponse, err := manageWork.CreateWork(ctx, connect.NewRequest(&managev1.CreateWorkRequest{
		Title: "Public Artist Label Work " + suffix, Slug: stringPointer("public-artist-label-work-" + suffix),
		Type: managev1.WorkType_WORK_TYPE_MUSIC_PROJECT, Year: 2026, Month: 4,
		IsPresent: &isPresent, Summary: &workSummary, Document: artistLabelEmptyWorkDocument("en"),
	}))
	require.NoError(t, err)
	creditRole := "Artist"
	_, err = manageWork.AddWorkCredit(ctx, connect.NewRequest(&managev1.AddWorkCreditRequest{
		WorkId: workResponse.Msg.Id, ArtistId: &mainArtist.Id, CreditRole: &creditRole,
	}))
	require.NoError(t, err)
	workImageFileID := seedArtistLabelFixtureFile(t, db, "work-image.webp", "image/webp", "image")
	workImage, err := manageWork.SetWorkFeaturedImage(ctx, connect.NewRequest(&managev1.SetWorkFeaturedImageRequest{
		WorkId: workResponse.Msg.Id, FileId: workImageFileID,
	}))
	require.NoError(t, err)
	require.Equal(t, AssetURL(t, db, workImageFileID), workImage.Msg.GetImageAsset().GetUrl())
	publishedWork, err := manageWork.PublishWork(ctx, connect.NewRequest(&managev1.PublishWorkRequest{Id: workResponse.Msg.Id}))
	require.NoError(t, err)
	require.Equal(t, managev1.WorkStatus_WORK_STATUS_PUBLISHED, publishedWork.Msg.Status)
	require.NoError(t, db.Model(&model.Work{}).Where("id = ?", workResponse.Msg.Id).
		Update("status", managev1.WorkStatus_WORK_STATUS_ARCHIVED.String()).Error)

	bareWorkResponse, err := manageWork.CreateWork(ctx, connect.NewRequest(&managev1.CreateWorkRequest{
		Title: "Public Artist Label Bare Work " + suffix,
		Slug:  stringPointer("public-artist-label-bare-work-" + suffix),
		Type:  managev1.WorkType_WORK_TYPE_MUSIC_PROJECT, Year: 2026, Month: 5,
		IsPresent: &isPresent, Document: artistLabelEmptyWorkDocument("en"),
	}))
	require.NoError(t, err)
	_, err = manageWork.AddWorkCredit(ctx, connect.NewRequest(&managev1.AddWorkCreditRequest{
		WorkId: bareWorkResponse.Msg.Id, ArtistId: &bareReleaseArtist.Id, CreditRole: &creditRole,
	}))
	require.NoError(t, err)
	bareWork, err := manageWork.PublishWork(ctx, connect.NewRequest(&managev1.PublishWorkRequest{Id: bareWorkResponse.Msg.Id}))
	require.NoError(t, err)
	require.Equal(t, managev1.WorkStatus_WORK_STATUS_PUBLISHED, bareWork.Msg.Status)

	return &ArtistLabelPublicFixture{
		DB: db, SpiceDB: spiceDB, AdminID: adminID, Context: ctx, Suffix: suffix,
		ArtistService: publicArtist, ArtistMedia: artistMedia, LabelService: publicLabel, ShareLinkService: shareLinks,
		ParentLabel: parentLabel, MainLabel: mainLabel, DraftLabel: draftLabel,
		DarkOnlyLabel: darkOnlyLabel, BareReleaseLabel: bareReleaseLabel,
		MainLabelDescription: mainLabelDescription, LabelLightLogoID: labelLightLogoID,
		LabelDarkLogoID: labelDarkLogoID, DarkOnlyLogoID: darkOnlyLogoID,
		MainLabelOGAssetID: mainLabelOGAssetID, MainLabelOGAssetURL: mainLabelOGAssetURL,
		MainArtist: mainArtist, OtherArtist: otherArtist, BareReleaseArtist: bareReleaseArtist,
		DraftArtist: draftArtist, MainArtistBio: mainArtistBio, ArtistImageFileID: artistImageFileID,
		MainArtistOGAssetID: mainArtistOGAssetID, MainArtistOGAssetURL: mainArtistOGAssetURL,
		Release: releaseResponse.Msg, BareReleaseID: bareRelease.Msg.Id, ReleaseArtworkFileID: releaseArtworkFileID,
		Work: workResponse.Msg, BareWorkID: bareWork.Msg.Id, WorkSummary: workSummary, WorkImageFileID: workImageFileID,
	}
}

func artistFixtureOGRefresher(db *gorm.DB) *og.Refresher {
	planner := og.NewPlanner(db, artistLabelPublicCDN, sitesettingsadapter.NewRenderConfig(), artistadapter.NewProjection())
	return og.NewRefresher(planner, og.NewResolver(artistadapter.NewRequests()))
}

func labelFixtureOGRefresher(db *gorm.DB) *og.Refresher {
	planner := og.NewPlanner(db, artistLabelPublicCDN, sitesettingsadapter.NewRenderConfig(), labeladapter.NewProjection())
	return og.NewRefresher(planner, og.NewResolver(labeladapter.NewRequests()))
}

func workFixtureRuntime(db *gorm.DB) *workadapter.Runtime {
	planner := og.NewPlanner(db, artistLabelPublicCDN, sitesettingsadapter.NewRenderConfig(), workadapter.NewProjection())
	return workadapter.NewRuntime(
		db,
		artistLabelPublicCDN,
		og.NewRefresher(planner, og.NewResolver(workadapter.NewRequests())),
	)
}

type artistLabelAsyncPublisher struct{}

func (artistLabelAsyncPublisher) EnqueueProtobuf(context.Context, string, string, proto.Message) error {
	return nil
}
func (artistLabelAsyncPublisher) NotifyProtobuf(context.Context, string, proto.Message) error {
	return nil
}
func (artistLabelAsyncPublisher) EnqueueProtobufWithExecutor(context.Context, eventpkg.DBTX, string, string, proto.Message) error {
	return nil
}

type artistLabelFileDeleter struct{}

func (artistLabelFileDeleter) DeleteFileByID(context.Context, string) error { return nil }
func (artistLabelFileDeleter) CleanupTrackUploadSessions(context.Context, string, string) error {
	return nil
}
func (artistLabelFileDeleter) RequireNoTrackUploadSessionsWithDB(context.Context, *gorm.DB, string) error {
	return nil
}

type artistLabelTranscoderPublisher struct{}

func (artistLabelTranscoderPublisher) PublishTranscodeAudio(context.Context, *managev1.TranscodeAudioEvent) error {
	return nil
}
func (artistLabelTranscoderPublisher) PublishTranscodeVideo(context.Context, *managev1.TranscodeVideoEvent) error {
	return nil
}
func (artistLabelTranscoderPublisher) PublishWaveformCancel(context.Context, *managev1.WaveformCancelEvent) error {
	return nil
}

type artistLabelMediaHydrator struct{}

func (artistLabelMediaHydrator) HydrateAuthorizedContentBlockMedia(
	_ context.Context,
	items []*contentv1.ContentBlockMediaItem,
) ([]*contentv1.ContentBlockMediaItem, error) {
	return items, nil
}

func (artistLabelMediaHydrator) HydrateAuthorizedWorkBlockMediaWithDB(
	_ context.Context,
	_ *gorm.DB,
	_ string,
	_ uuid.UUID,
	_ *auth.UserInfo,
	items []*contentv1.ContentBlockMediaItem,
) ([]*contentv1.ContentBlockMediaItem, error) {
	return items, nil
}

func createDraftArtistLabelFixtureArtist(
	t *testing.T,
	service *artistdomain.ArtistService,
	ctx context.Context,
	name string,
	slug string,
	bioOverride ...string,
) *managev1.Artist {
	t.Helper()
	bio := name + " bio"
	if len(bioOverride) > 0 {
		bio = bioOverride[0]
	}
	realName := name + " Real"
	countryCode := "KR"
	website := "https://example.com/" + slug
	response, err := service.CreateArtist(ctx, connect.NewRequest(&managev1.CreateArtistRequest{
		Name: name, Slug: &slug, RealName: &realName,
		Document: testutil.CreativeContentDocument("en", bio), CountryCode: &countryCode, Website: &website,
		SocialLinks: map[string]string{"homepage": "https://social.example.com/" + slug},
	}))
	require.NoError(t, err)
	return response.Msg
}

func createPublishedArtistLabelFixtureArtist(
	t *testing.T,
	service *artistdomain.ArtistService,
	ctx context.Context,
	name string,
	slug string,
	bioOverride ...string,
) *managev1.Artist {
	t.Helper()
	created := createDraftArtistLabelFixtureArtist(t, service, ctx, name, slug, bioOverride...)
	response, err := service.PublishArtist(ctx, connect.NewRequest(&managev1.PublishArtistRequest{Id: created.Id}))
	require.NoError(t, err)
	created.Status = response.Msg.Status.String()
	created.PublishedAt = response.Msg.PublishedAt
	created.UpdatedAt = response.Msg.UpdatedAt
	return created
}

func createDraftArtistLabelFixtureLabel(
	t *testing.T,
	service *labeldomain.LabelService,
	ctx context.Context,
	name string,
	slug string,
	parentLabelID *string,
	descriptionOverride ...string,
) *managev1.Label {
	t.Helper()
	description := name + " description"
	if len(descriptionOverride) > 0 {
		description = descriptionOverride[0]
	}
	countryCode := "KR"
	website := "https://example.com/" + slug
	response, err := service.CreateLabel(ctx, connect.NewRequest(&managev1.CreateLabelRequest{
		Name: name, Slug: &slug, Document: testutil.CreativeContentDocument("en", description),
		CountryCode: &countryCode, Website: &website,
		SocialLinks:   map[string]string{"homepage": "https://social.example.com/" + slug},
		ParentLabelId: parentLabelID,
	}))
	require.NoError(t, err)
	return response.Msg
}

func createPublishedArtistLabelFixtureLabel(
	t *testing.T,
	service *labeldomain.LabelService,
	ctx context.Context,
	name string,
	slug string,
	parentLabelID *string,
	descriptionOverride ...string,
) *managev1.Label {
	t.Helper()
	created := createDraftArtistLabelFixtureLabel(t, service, ctx, name, slug, parentLabelID, descriptionOverride...)
	response, err := service.PublishLabel(ctx, connect.NewRequest(&managev1.PublishLabelRequest{Id: created.Id}))
	require.NoError(t, err)
	created.Status = response.Msg.Status.String()
	created.PublishedAt = response.Msg.PublishedAt
	created.UpdatedAt = response.Msg.UpdatedAt
	return created
}

func linkArtistLabelFixture(
	t *testing.T,
	db *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	store *contentblock.Store,
	publisher artistLabelAsyncPublisher,
	ctx context.Context,
	artistID string,
	labelID string,
) {
	t.Helper()
	var state struct {
		Revision    string `gorm:"column:revision"`
		Contributor string `gorm:"column:contributor"`
	}
	require.NoError(t, db.Raw(`
		SELECT document.revision::text AS revision, owner.member_id::text AS contributor
		FROM artist
		JOIN content_document AS document ON document.id = artist.content_document_id
		JOIN artist_owner AS owner ON owner.artist_id = artist.id
		WHERE artist.id = ?::uuid
		LIMIT 1
	`, artistID).Scan(&state).Error)
	_, err := artistdomain.NewInternalArtistService(
		db, publisher, spiceDB,
		artistdomain.Dependencies{
			Translation: artistadapter.NewTranslation(),
			Runtime:     artistadapter.NewRuntime(artistLabelPublicCDN, artistFixtureOGRefresher(db)),
		},
		artistdomain.WithInternalArtistContentBlockStore(store),
	).UpdateArtistDocumentMetadata(ctx, connect.NewRequest(&intrav1.UpdateArtistDocumentMetadataRequest{
		ArtistId: artistID, ExpectedRevision: state.Revision, Locale: "en",
		ContributorMemberIds: []string{state.Contributor},
		Update: &intrav1.ArtistDocumentMetadataUpdate{
			LabelIds: &intrav1.ArtistLabelIdsValue{Values: []string{labelID}},
		},
	}))
	require.NoError(t, err)
}

func artistLabelEmptyWorkDocument(locale string) *contentv1.RichTextDocument {
	return &contentv1.RichTextDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_WORK,
		SourceLocale:            locale, Base: &contentv1.RichTextBlockGraph{},
		LocaleOverlays: []*contentv1.RichTextLocaleOverlay{{Locale: locale}},
	}
}

func stringPointer(value string) *string { return &value }

func seedArtistLabelFixtureFile(t *testing.T, db *gorm.DB, fileName, mimeType, assetKind string) string {
	t.Helper()
	fileID := uuid.NewString()
	extension := model.GetExtensionFromMime(mimeType)
	fileName = strings.TrimSuffix(filepath.Base(fileName), filepath.Ext(fileName))
	now := time.Now().UTC()
	require.NoError(t, db.Create(&model.File{
		ID: fileID, FileName: fileName, MimeType: mimeType, FileSize: 1024,
		Extension: extension, SHA256: make([]byte, 32), CreatedAt: now,
	}).Error)
	seedArtistLabelFixtureAsset(t, db, fileID, assetKind)
	return fileID
}

func seedArtistLabelFixtureAsset(t *testing.T, db *gorm.DB, fileID, kind string) string {
	t.Helper()
	var file model.File
	require.NoError(t, db.Where("id = ?", fileID).Take(&file).Error)
	assetID := uuid.NewString()
	objectKey, err := mediaauth.AssetObjectKey(assetID, file.Extension)
	require.NoError(t, err)
	now := time.Now().UTC()
	fileSize := file.FileSize
	require.NoError(t, db.Create(&model.PublicAsset{
		ID: assetID, SourceFileID: &fileID, Kind: kind, ObjectKey: objectKey,
		Extension: file.Extension, MimeType: file.MimeType, FileSize: &fileSize,
		SHA256: append([]byte(nil), file.SHA256...), Disposition: "inline",
		Status: model.PublicAssetStatusReady, ReadyAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error)
	return assetID
}

func seedArtistLabelFixtureOGAsset(t *testing.T, db *gorm.DB, ownerType, ownerID string) (string, string) {
	t.Helper()
	fileID := seedArtistLabelFixtureFile(t, db, "og.webp", "image/webp", "og")
	var asset model.PublicAsset
	require.NoError(t, db.Where("source_file_id = ?", fileID).Take(&asset).Error)
	now := time.Now().UTC()
	require.NoError(t, db.Create(&model.PublicAssetBinding{
		AssetID: asset.ID, OwnerType: ownerType, OwnerID: ownerID, BindingKey: "og",
		SourceFileID: &fileID, CreatedAt: now, UpdatedAt: now,
	}).Error)
	return asset.ID, AssetURL(t, db, fileID)
}

func AssetURL(t *testing.T, db *gorm.DB, fileID string) string {
	t.Helper()
	var asset model.PublicAsset
	require.NoError(t, db.Where("source_file_id = ? AND status = ?", fileID, model.PublicAssetStatusReady).Take(&asset).Error)
	assetPath, err := mediaauth.AssetPath(asset.ID, asset.Kind, asset.Extension)
	require.NoError(t, err)
	return strings.TrimRight(artistLabelPublicCDN, "/") + "/" + strings.TrimLeft(assetPath, "/")
}

func LocalizedDocumentText(document *contentv1.LocalizedRichTextDocument) string {
	if document == nil || document.LocaleOverlay == nil || len(document.LocaleOverlay.Blocks) != 1 {
		return ""
	}
	paragraph := document.LocaleOverlay.Blocks[0].GetParagraph()
	if paragraph == nil || len(paragraph.Content) != 1 || paragraph.Content[0].GetText() == nil {
		return ""
	}
	return paragraph.Content[0].GetText().GetText()
}
