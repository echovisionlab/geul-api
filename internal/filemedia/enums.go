package filemedia

import "github.com/echovisionlab/geul-api/internal/filemedia/domain"

type UploadType = domain.UploadType

const (
	UploadUnspecified         = domain.UploadUnspecified
	UploadUserAvatar          = domain.UploadUserAvatar
	UploadArtistImage         = domain.UploadArtistImage
	UploadEditorImage         = domain.UploadEditorImage
	UploadEditorVideo         = domain.UploadEditorVideo
	UploadEditorAudio         = domain.UploadEditorAudio
	UploadEditorAttachment    = domain.UploadEditorAttachment
	UploadFeaturedImage       = domain.UploadFeaturedImage
	UploadWorkFeaturedImage   = domain.UploadWorkFeaturedImage
	UploadReleaseArtwork      = domain.UploadReleaseArtwork
	UploadTrackAudio          = domain.UploadTrackAudio
	UploadLabelImage          = domain.UploadLabelImage
	UploadClientLogo          = domain.UploadClientLogo
	UploadSiteLogo            = domain.UploadSiteLogo
	UploadSiteFavicon         = domain.UploadSiteFavicon
	UploadSiteLoader          = domain.UploadSiteLoader
	UploadSeriesFeaturedImage = domain.UploadSeriesFeaturedImage
	UploadFormFeaturedImage   = domain.UploadFormFeaturedImage
	UploadSiteOGBackground    = domain.UploadSiteOGBackground
	UploadProgramEventPoster  = domain.UploadProgramEventPoster
	UploadEditorMesh          = domain.UploadEditorMesh
	UploadMapImage            = domain.UploadMapImage
	UploadGeneralFile         = domain.UploadGeneralFile
)

type EntityType = domain.EntityType

const (
	EntityUnspecified  = domain.EntityUnspecified
	EntityPost         = domain.EntityPost
	EntityPage         = domain.EntityPage
	EntityWork         = domain.EntityWork
	EntityTrack        = domain.EntityTrack
	EntityProgramEvent = domain.EntityProgramEvent
	EntityFile         = domain.EntityFile
)

type IngestSource = domain.IngestSource

const (
	IngestSourceUnspecified  = domain.IngestSourceUnspecified
	IngestSourceDirectUpload = domain.IngestSourceDirectUpload
	IngestSourceRemoteURL    = domain.IngestSourceRemoteURL
)

type IngestMediaKind = domain.IngestMediaKind

const (
	IngestMediaUnspecified      = domain.IngestMediaUnspecified
	IngestMediaEditorImage      = domain.IngestMediaEditorImage
	IngestMediaEditorAudio      = domain.IngestMediaEditorAudio
	IngestMediaEditorVideo      = domain.IngestMediaEditorVideo
	IngestMediaEditorAttachment = domain.IngestMediaEditorAttachment
	IngestMediaTrackAudio       = domain.IngestMediaTrackAudio
	IngestMediaOther            = domain.IngestMediaOther
	IngestMediaEditorMesh       = domain.IngestMediaEditorMesh
)

type IngestFailureReason = domain.IngestFailureReason

const (
	IngestFailureUnspecified   = domain.IngestFailureUnspecified
	IngestFailureAborted       = domain.IngestFailureAborted
	IngestFailureRejected      = domain.IngestFailureRejected
	IngestFailureStorageFailed = domain.IngestFailureStorageFailed
	IngestFailureInternal      = domain.IngestFailureInternal
	IngestFailureExpired       = domain.IngestFailureExpired
)

type ClientMediaKind = domain.ClientMediaKind

const (
	ClientMediaUnspecified = domain.ClientMediaUnspecified
	ClientMediaAudio       = domain.ClientMediaAudio
	ClientMediaVideo       = domain.ClientMediaVideo
)

type MeshCompressionMethod = domain.MeshCompressionMethod

const (
	MeshCompressionUnspecified = domain.MeshCompressionUnspecified
	MeshCompressionDraco       = domain.MeshCompressionDraco
)

type MeshProfile = domain.MeshProfile

const (
	MeshProfileUnspecified    = domain.MeshProfileUnspecified
	MeshProfileDracoWebpV1    = domain.MeshProfileDracoWebpV1
	MeshProfileParticleMeshV1 = domain.MeshProfileParticleMeshV1
)

type LibrarySortField = domain.LibrarySortField

const (
	LibrarySortUnspecified = domain.LibrarySortUnspecified
	LibrarySortName        = domain.LibrarySortName
	LibrarySortCreatedAt   = domain.LibrarySortCreatedAt
	LibrarySortUpdatedAt   = domain.LibrarySortUpdatedAt
	LibrarySortFileSize    = domain.LibrarySortFileSize
)

type SortOrder = domain.SortOrder

const (
	SortUnspecified = domain.SortUnspecified
	SortAsc         = domain.SortAsc
	SortDesc        = domain.SortDesc
)

type DownloadAudience = domain.DownloadAudience

const (
	DownloadAudienceUnspecified   = domain.DownloadAudienceUnspecified
	DownloadAudienceDisabled      = domain.DownloadAudienceDisabled
	DownloadAudiencePublic        = domain.DownloadAudiencePublic
	DownloadAudienceAuthenticated = domain.DownloadAudienceAuthenticated
	DownloadAudienceRestricted    = domain.DownloadAudienceRestricted
)
