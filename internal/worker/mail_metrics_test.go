package worker

import (
	"context"
	"testing"
	"time"

	emaildeliveryadapter "github.com/echovisionlab/geul-api/internal/adapters/emaildelivery"
	"github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/emailauthoring"
	"github.com/echovisionlab/geul-api/internal/model"
)

func TestMailTemplateClassUsesEmailAuthoringClassifier(t *testing.T) {
	tests := []struct {
		name         string
		templateType string
		want         string
	}{
		{
			name:         "campaign template",
			templateType: "campaign:welcome",
			want:         string(emailauthoring.EmailTemplateClassCampaign),
		},
		{
			name:         "direct test template",
			templateType: "template:probe",
			want:         string(emailauthoring.EmailTemplateClassTestDirect),
		},
		{
			name:         "unknown template",
			templateType: "unregistered",
			want:         string(emailauthoring.EmailTemplateClassUnknown),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := emaildeliveryadapter.TemplateClass(tt.templateType); got != tt.want {
				t.Fatalf("TemplateClass() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMailMetricsRecordMethodsTolerateNilCounters(t *testing.T) {
	metrics := emaildeliveryadapter.Metrics{}
	ctx := context.Background()

	metrics.RecordSendAttempt(ctx, "campaign:announcement")
	metrics.RecordSendResult(ctx, "campaign:announcement", "success")
	metrics.RecordRecipientStatus(ctx, "campaign:announcement", campaign.CampaignDeliveryRecipientStatusSent)
	metrics.RecordRunDuration(ctx, model.CampaignDeliveryRun{}, time.Now().UTC())
}
