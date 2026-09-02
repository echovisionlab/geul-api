package worker

import (
	"context"
	"testing"
	"time"

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
			if got := mailTemplateClass(tt.templateType); got != tt.want {
				t.Fatalf("mailTemplateClass() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMailMetricsRecordMethodsTolerateNilCounters(t *testing.T) {
	metrics := mailMetrics{}
	ctx := context.Background()

	metrics.recordSendAttempt(ctx, "campaign:announcement")
	metrics.recordSendResult(ctx, "campaign:announcement", "success")
	metrics.recordRecipientStatus(ctx, "campaign:announcement", campaign.CampaignDeliveryRecipientStatusSent)
	metrics.RecordRunDuration(ctx, model.CampaignDeliveryRun{}, time.Now().UTC())
}
