package worker

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/mq"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestRequireStableDeliveryMessageID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		messageID string
		payloadID string
		wantClass string
	}{
		{name: "matching", messageID: "stable-1", payloadID: "stable-1"},
		{name: "payload id missing", messageID: "stable-1", wantClass: "missing_payload_message_id"},
		{name: "transport id missing", payloadID: "stable-1", wantClass: "missing_transport_message_id"},
		{name: "mismatch", messageID: "envelope-1", payloadID: "payload-1", wantClass: "transport_payload_id_mismatch"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := requireStableDeliveryMessageID(mq.Message{MessageID: tt.messageID}, tt.payloadID)
			if tt.wantClass == "" {
				require.NoError(t, err)
				return
			}

			require.Error(t, err)
			class, terminal := mq.TerminalDeliveryErrorClass(err)
			require.True(t, terminal)
			require.Equal(t, tt.wantClass, class)
		})
	}
}

func TestUserDeleteAvatarRejectsInvalidAssetIDAsTerminalPoison(t *testing.T) {
	memberID := "14600000-0000-1000-8000-000000000001"
	body, err := proto.Marshal(&managev1.UserDeleteAvatarCommand{
		MemberId:      memberID,
		AvatarAssetId: new("not-a-uuid"),
	})
	require.NoError(t, err)

	err = (&Handlers{}).handleUserDeleteAvatarMessage(t.Context(), mq.Message{
		Body:      body,
		MessageID: "user-delete-avatar:" + memberID,
	})
	require.Error(t, err)
	class, terminal := mq.TerminalDeliveryErrorClass(err)
	require.True(t, terminal)
	require.Equal(t, "invalid_user_delete_avatar", class)
}
