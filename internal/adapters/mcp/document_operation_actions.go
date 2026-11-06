package mcp

import (
	"context"
	"time"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	pagedomain "github.com/echovisionlab/geul-api/internal/page"
	postdomain "github.com/echovisionlab/geul-api/internal/post"
	"github.com/echovisionlab/geul-api/internal/programevent"
	releasedomain "github.com/echovisionlab/geul-api/internal/release"
	"github.com/echovisionlab/geul-api/internal/work"
)

// PostOperationApplication is the Post use cases behind the shared document tools.
type PostOperationApplication interface {
	GetPost(context.Context, postdomain.GetPostQuery) (postdomain.View, error)
	PublishPost(context.Context, postdomain.PostLifecycleCommand) (postdomain.Lifecycle, error)
	UnpublishPost(context.Context, postdomain.PostLifecycleCommand) (postdomain.Lifecycle, error)
	ArchivePost(context.Context, postdomain.PostLifecycleCommand) (postdomain.Lifecycle, error)
	SchedulePost(context.Context, postdomain.SchedulePostCommand) (postdomain.Lifecycle, error)
	CancelPostSchedule(context.Context, postdomain.PostLifecycleCommand) (postdomain.Lifecycle, error)
	RepublishPost(context.Context, postdomain.PostLifecycleCommand) (postdomain.Lifecycle, error)
	DeletePost(context.Context, postdomain.DeletePostCommand) error
	CheckSlugAvailable(context.Context, postdomain.CheckSlugAvailableQuery) (bool, error)
}

// WorkOperationApplication is the Work use cases behind the shared document tools.
type WorkOperationApplication interface {
	GetWork(context.Context, work.GetWorkQuery) (work.View, error)
	PublishWork(context.Context, work.PublishWorkCommand) (work.LifecycleChange, error)
	UnpublishWork(context.Context, work.UnpublishWorkCommand) (work.LifecycleChange, error)
	DeleteWork(context.Context, work.DeleteWorkCommand) error
	CheckWorkSlugAvailable(context.Context, work.CheckWorkSlugAvailableQuery) (bool, error)
}

// PageOperationApplication is the Page use cases behind the shared document tools.
type PageOperationApplication interface {
	GetPage(context.Context, pagedomain.GetPageQuery) (pagedomain.Detail, error)
	PublishPage(context.Context, pagedomain.PublishPageCommand) (pagedomain.LifecycleResult, error)
	UnpublishPage(context.Context, pagedomain.UnpublishPageCommand) (pagedomain.LifecycleResult, error)
	DeletePage(context.Context, pagedomain.DeletePageCommand) error
	CheckPageSlugAvailable(context.Context, pagedomain.CheckPageSlugAvailableQuery) (bool, error)
}

// ReleaseOperationApplication is the Release use cases behind the shared document
// tools.
type ReleaseOperationApplication interface {
	GetRelease(context.Context, releasedomain.GetReleaseQuery) (releasedomain.View, error)
	PublishRelease(context.Context, releasedomain.PublishReleaseCommand) (releasedomain.LifecycleChange, error)
	UnpublishRelease(context.Context, releasedomain.UnpublishReleaseCommand) (releasedomain.LifecycleChange, error)
	DeleteRelease(context.Context, releasedomain.DeleteReleaseCommand) error
	CheckReleaseSlugAvailable(context.Context, releasedomain.CheckReleaseSlugQuery) (bool, error)
}

// ProgramEventOperationApplication is the Program Event use cases behind the shared
// document tools.
type ProgramEventOperationApplication interface {
	GetProgramEvent(context.Context, programevent.GetProgramEventQuery) (programevent.EventView, error)
	PublishProgramEvent(context.Context, programevent.PublishProgramEventCommand) (programevent.LifecycleChange, error)
	ArchiveProgramEvent(context.Context, programevent.ArchiveProgramEventCommand) (programevent.LifecycleChange, error)
	DeleteProgramEvent(context.Context, programevent.DeleteProgramEventCommand) error
}

// documentAction changes one document and reports whether anything changed.
type documentAction func(ctx context.Context, documentID string) (changed bool, err error)

// documentActionSchedule is the one publication action that takes a time.
const documentActionSchedule = "schedule"

// postSchedule is when the schedule action publishes a Post.
type postSchedule struct {
	at       time.Time
	timeZone string
}

// postLifecycleAction runs a Post transition. A Post use case does not report a
// transition that changed nothing, so every success reads as changed, as the manage
// API reports it.
func postLifecycleAction(transition func(context.Context, postdomain.PostLifecycleCommand) (postdomain.Lifecycle, error)) documentAction {
	return func(ctx context.Context, documentID string) (bool, error) {
		_, err := transition(ctx, postdomain.PostLifecycleCommand{ID: documentID})
		return err == nil, err
	}
}

// publicationActions names the action behind each publication action and document
// type. An absent pair is an action that type does not have.
func (tools *DocumentOperationTools) publicationActions(schedule postSchedule) map[string]map[core.Domain]documentAction {
	posts, works, pages, releases, events := tools.applications.Posts, tools.applications.Works, tools.applications.Pages, tools.applications.Releases, tools.applications.Events
	return map[string]map[core.Domain]documentAction{
		"publish": {
			core.DomainPost: postLifecycleAction(posts.PublishPost),
			core.DomainPage: func(ctx context.Context, documentID string) (bool, error) {
				result, err := pages.PublishPage(ctx, pagedomain.PublishPageCommand{ID: documentID})
				return result.Changed, err
			},
			core.DomainWork: func(ctx context.Context, documentID string) (bool, error) {
				change, err := works.PublishWork(ctx, work.PublishWorkCommand{ID: documentID})
				return change.Changed, err
			},
			core.DomainRelease: func(ctx context.Context, documentID string) (bool, error) {
				change, err := releases.PublishRelease(ctx, releasedomain.PublishReleaseCommand{ID: documentID})
				return change.Changed, err
			},
			core.DomainProgramEvent: func(ctx context.Context, documentID string) (bool, error) {
				change, err := events.PublishProgramEvent(ctx, programevent.PublishProgramEventCommand{ID: documentID})
				return change.Changed, err
			},
		},
		"unpublish": {
			core.DomainPost: postLifecycleAction(posts.UnpublishPost),
			core.DomainPage: func(ctx context.Context, documentID string) (bool, error) {
				result, err := pages.UnpublishPage(ctx, pagedomain.UnpublishPageCommand{ID: documentID})
				return result.Changed, err
			},
			core.DomainWork: func(ctx context.Context, documentID string) (bool, error) {
				change, err := works.UnpublishWork(ctx, work.UnpublishWorkCommand{ID: documentID})
				return change.Changed, err
			},
			core.DomainRelease: func(ctx context.Context, documentID string) (bool, error) {
				change, err := releases.UnpublishRelease(ctx, releasedomain.UnpublishReleaseCommand{ID: documentID})
				return change.Changed, err
			},
		},
		"archive": {
			core.DomainPost: postLifecycleAction(posts.ArchivePost),
			core.DomainProgramEvent: func(ctx context.Context, documentID string) (bool, error) {
				change, err := events.ArchiveProgramEvent(ctx, programevent.ArchiveProgramEventCommand{ID: documentID})
				return change.Changed, err
			},
		},
		"republish": {core.DomainPost: postLifecycleAction(posts.RepublishPost)},
		documentActionSchedule: {
			core.DomainPost: func(ctx context.Context, documentID string) (bool, error) {
				_, err := posts.SchedulePost(ctx, postdomain.SchedulePostCommand{ID: documentID, ScheduledAt: &schedule.at, TimeZone: schedule.timeZone})
				return err == nil, err
			},
		},
		"cancel_schedule": {core.DomainPost: postLifecycleAction(posts.CancelPostSchedule)},
	}
}

// deleteActions names the delete of each document type. A delete that succeeds
// always changed something.
func (tools *DocumentOperationTools) deleteActions() map[core.Domain]documentAction {
	posts, works, pages, releases, events := tools.applications.Posts, tools.applications.Works, tools.applications.Pages, tools.applications.Releases, tools.applications.Events
	return map[core.Domain]documentAction{
		core.DomainPost: func(ctx context.Context, documentID string) (bool, error) {
			err := posts.DeletePost(ctx, postdomain.DeletePostCommand{ID: documentID})
			return err == nil, err
		},
		core.DomainPage: func(ctx context.Context, documentID string) (bool, error) {
			err := pages.DeletePage(ctx, pagedomain.DeletePageCommand{ID: documentID})
			return err == nil, err
		},
		core.DomainWork: func(ctx context.Context, documentID string) (bool, error) {
			err := works.DeleteWork(ctx, work.DeleteWorkCommand{ID: documentID})
			return err == nil, err
		},
		core.DomainRelease: func(ctx context.Context, documentID string) (bool, error) {
			err := releases.DeleteRelease(ctx, releasedomain.DeleteReleaseCommand{ID: documentID})
			return err == nil, err
		},
		core.DomainProgramEvent: func(ctx context.Context, documentID string) (bool, error) {
			err := events.DeleteProgramEvent(ctx, programevent.DeleteProgramEventCommand{ID: documentID})
			return err == nil, err
		},
	}
}
