package httpapi

import (
	"strings"
	"testing"
	"time"
)

func TestDecodeLegnextCallbackNormalizesMinimalFields(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantID     string
		wantStatus string
	}{
		{
			name:       "top level task id",
			body:       `{"task_id":"task-1","status":"COMPLETED","output":{"image":"data:image/png;base64,must-not-survive"},"secret":"must-not-survive"}`,
			wantID:     "task-1",
			wantStatus: "completed",
		},
		{
			name:       "nested data",
			body:       `{"data":{"job_id":"job-2","status":"processing","arbitrary":{"large":"value"}}}`,
			wantID:     "job-2",
			wantStatus: "processing",
		},
		{
			name:       "unknown status",
			body:       `{"id":"job-3","status":"provider-specific-message"}`,
			wantID:     "job-3",
			wantStatus: "unknown",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			callback, err := decodeLegnextCallback([]byte(test.body))
			if err != nil {
				t.Fatalf("decodeLegnextCallback: %v", err)
			}
			if callback.ProviderJobID != test.wantID || callback.Status != test.wantStatus {
				t.Fatalf("callback = %+v", callback)
			}
		})
	}
}

func TestDecodeLegnextCallbackRejectsInvalidShapeAndIdentifier(t *testing.T) {
	for name, body := range map[string]string{
		"array":            `[]`,
		"invalid JSON":     `{`,
		"control in id":    `{"job_id":"job\n1"}`,
		"oversized job id": `{"job_id":"` + strings.Repeat("a", 257) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeLegnextCallback([]byte(body)); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestCallbackLifecycleWindow(t *testing.T) {
	now := time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC)
	activeDeadline := now.Add(-legnextCallbackGrace + time.Second)
	expiredDeadline := now.Add(-legnextCallbackGrace - time.Second)
	if callbackExpired(now, &activeDeadline, now.Add(-48*time.Hour)) {
		t.Fatal("deadline inside grace period was rejected")
	}
	if !callbackExpired(now, &expiredDeadline, now) {
		t.Fatal("deadline outside grace period was accepted")
	}
	if callbackExpired(now, nil, now.Add(-legacyCallbackLifetime+time.Second)) {
		t.Fatal("legacy callback inside lifetime was rejected")
	}
	if !callbackExpired(now, nil, now.Add(-legacyCallbackLifetime-time.Second)) {
		t.Fatal("legacy callback outside lifetime was accepted")
	}
	for _, status := range []string{"succeeded", "failed", "cancelled"} {
		if callbackJobIsActive(status) {
			t.Fatalf("terminal status %q is active", status)
		}
	}
	for _, status := range []string{"submitting", "submission_uncertain", "provider_pending", "ingesting", "cancelling"} {
		if !callbackJobIsActive(status) {
			t.Fatalf("non-terminal status %q is inactive", status)
		}
	}
}

func TestCallbackLimitsStayBounded(t *testing.T) {
	if maxLegnextCallbackBody != 64<<10 {
		t.Fatalf("callback body limit = %d", maxLegnextCallbackBody)
	}
	if maxLegnextCallbackEvents != 32 {
		t.Fatalf("callback event limit = %d", maxLegnextCallbackEvents)
	}
}
