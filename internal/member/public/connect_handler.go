package public

import (
	"context"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/member"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/open/v1/openv1connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func respond[T any](message *T, err error) (*connect.Response[T], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(message), nil
}

// MemberConnectHandler serves the public Member Connect RPCs.
type MemberConnectHandler struct {
	openv1connect.UnimplementedMemberServiceHandler
	members *Service
}

// NewMemberConnectHandler returns the public Member Connect adapter of members.
func NewMemberConnectHandler(members *Service) *MemberConnectHandler {
	return &MemberConnectHandler{members: members}
}

func profileMessage(profile *Profile) *openv1.PublicMemberProfile {
	if profile == nil {
		return nil
	}
	return &openv1.PublicMemberProfile{
		Summary: member.SummaryMessage(profile.Summary), Bio: profile.Bio, Website: profile.Website,
		SocialLinks: profile.SocialLinks, CreatedAt: timestamppb.New(profile.CreatedAt),
	}
}

func (h *MemberConnectHandler) GetPublicMember(ctx context.Context, req *connect.Request[openv1.GetPublicMemberRequest]) (*connect.Response[openv1.GetPublicMemberResponse], error) {
	profile, err := h.members.GetPublicMember(ctx, GetPublicMemberQuery{MemberID: req.Msg.GetMemberId()})
	return respond(&openv1.GetPublicMemberResponse{Member: profileMessage(profile)}, err)
}

func (h *MemberConnectHandler) ListAuthors(ctx context.Context, req *connect.Request[openv1.ListAuthorsRequest]) (*connect.Response[openv1.ListAuthorsResponse], error) {
	authors, err := h.members.ListAuthors(ctx, ListAuthorsQuery{Limit: req.Msg.GetLimit(), MemberIDs: req.Msg.MemberIds})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	messages := make([]*openv1.PublicAuthorSummary, len(authors))
	for i, author := range authors {
		messages[i] = &openv1.PublicAuthorSummary{Member: member.SummaryMessage(author.Member), PostCount: author.PostCount, Bio: author.Bio}
	}
	return connect.NewResponse(&openv1.ListAuthorsResponse{Authors: messages}), nil
}

// NewsletterConnectHandler serves the public newsletter Connect RPCs.
type NewsletterConnectHandler struct {
	openv1connect.UnimplementedNewsletterServiceHandler
	members *Service
}

// NewNewsletterConnectHandler returns the newsletter Connect adapter of members.
func NewNewsletterConnectHandler(members *Service) *NewsletterConnectHandler {
	return &NewsletterConnectHandler{members: members}
}

func (h *NewsletterConnectHandler) Unsubscribe(ctx context.Context, req *connect.Request[openv1.UnsubscribeNewsletterRequest]) (*connect.Response[openv1.UnsubscribeNewsletterResponse], error) {
	err := h.members.UnsubscribeNewsletter(ctx, UnsubscribeNewsletterCommand{Token: req.Msg.GetToken()})
	return respond(&openv1.UnsubscribeNewsletterResponse{Success: true}, err)
}
