package member

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/DATA-DOG/go-sqlmock"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type unchangedAvatarPromoter struct{ FileDeleter }

func (unchangedAvatarPromoter) PromoteUserAvatarAsset(context.Context, string) (*commonv1.AssetRef, error) {
	return &commonv1.AssetRef{AssetId: "44444444-4444-4444-8444-444444444444"}, nil
}

func TestAvatarMutationDistinguishesMissingMemberFromProjectionFailure(t *testing.T) {
	memberID := "11111111-1111-4111-8111-111111111111"
	identityID := "22222222-2222-4222-8222-222222222222"
	for _, set := range []bool{false, true} {
		for _, missing := range []bool{false, true} {
			name := "delete"
			if set {
				name = "set"
			}
			if missing {
				name += "/missing"
			} else {
				name += "/database failure"
			}
			t.Run(name, func(t *testing.T) {
				sqlDB, mock, err := sqlmock.New()
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
				db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
				require.NoError(t, err)
				targetRows := func() *sqlmock.Rows {
					return sqlmock.NewRows([]string{"member_id", "identity_id", "onboarded"}).AddRow(memberID, identityID, true)
				}
				mock.ExpectQuery(`SELECT member.id::text AS member_id`).WillReturnRows(targetRows())
				mock.ExpectExec(`SELECT pg_advisory_lock\(`).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectExec(`SELECT pg_advisory_lock\(hashtextextended`).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectBegin()
				mock.ExpectQuery(`SELECT member.id::text AS member_id`).WillReturnRows(targetRows())
				if set {
					mock.ExpectQuery(`SELECT count\(\*\) FROM "file_ingest_binding"`).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
					mock.ExpectQuery(`SELECT .* FROM "public_asset_binding"`).WillReturnRows(sqlmock.NewRows([]string{"asset_id"}).AddRow("44444444-4444-4444-8444-444444444444"))
				} else {
					mock.ExpectQuery(`SELECT .* FROM "public_asset_binding"`).WillReturnRows(sqlmock.NewRows([]string{"asset_id"}))
				}
				mock.ExpectCommit()
				mock.ExpectQuery(`SELECT pg_advisory_unlock\(hashtextextended`).WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(true))
				mock.ExpectQuery(`SELECT pg_advisory_unlock\(`).WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(true))
				lookupErr := errors.New("member projection unavailable")
				query := mock.ExpectQuery(`SELECT .* FROM "member"`)
				want := connect.CodeInternal
				if missing {
					query.WillReturnRows(sqlmock.NewRows([]string{"id"}))
					want = connect.CodeNotFound
				} else {
					query.WillReturnError(lookupErr)
				}
				service := &MemberService{db: db, fileService: unchangedAvatarPromoter{}}
				var summary *commonv1.MemberSummary
				if set {
					summary, err = service.setAvatar(t.Context(), memberID, "33333333-3333-4333-8333-333333333333", false)
				} else {
					summary, err = service.deleteAvatar(t.Context(), memberID, false)
				}
				require.Nil(t, summary)
				require.Equal(t, want, connect.CodeOf(err))
				if !missing {
					require.ErrorIs(t, err, lookupErr)
				}
				require.NoError(t, mock.ExpectationsWereMet())
				mock.ExpectClose()
			})
		}
	}
}
