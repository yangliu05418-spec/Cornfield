package provider

import (
	"context"
	"fmt"
	"time"
)

type CanonicalRequest struct {
	JobID             string
	Model             string
	Prompt            string
	AspectRatio       string
	PromptAspectRatio bool
	Resolution        string
	ExpectedImages    int
	ReferenceData     []string
	ReferenceURLs     []string
	RequestParameters []string
	CallbackURL       string
	Options           GenerationOptions
}

type GenerationOptions struct {
	Midjourney *MidjourneyOptions `json:"midjourney,omitempty"`
	Image      *ImageOptions      `json:"image,omitempty"`
}

type ImageOptions struct {
	Quality string `json:"quality,omitempty"`
}

type MidjourneyOptions struct {
	Version     string   `json:"version"`
	Resolution  string   `json:"resolution,omitempty"`
	Speed       string   `json:"speed"`
	Quality     *int     `json:"quality,omitempty"`
	Draft       bool     `json:"draft"`
	Stylize     int      `json:"stylize"`
	Chaos       int      `json:"chaos"`
	Weird       int      `json:"weird"`
	Raw         bool     `json:"raw"`
	Tile        bool     `json:"tile"`
	ImageWeight *float64 `json:"image_weight,omitempty"`
}

type Submission struct {
	ProviderJobID string
	PollingURL    string
	Completed     bool
	Result        Result
	Telemetry     Telemetry
}

type Result struct {
	Status    string
	Images    []Image
	Usage     map[string]any
	ErrorCode string
	ErrorText string
	Telemetry Telemetry
}

// Telemetry contains bounded, non-secret transport metadata that can be used
// to correlate an attempt with a provider's support logs.
type Telemetry struct {
	ProviderRequestID string
	HTTPStatus        int
}

func (t Telemetry) Normalized() Telemetry {
	t.ProviderRequestID = normalizeProviderRequestID(t.ProviderRequestID)
	if t.HTTPStatus < 100 || t.HTTPStatus > 599 {
		t.HTTPStatus = 0
	}
	return t
}

type Image struct {
	Bytes     []byte
	URL       string
	MediaType string
}

type CancelResult struct {
	Accepted  bool
	Mode      string
	Telemetry Telemetry
}

type Health struct {
	Healthy bool
	Message string
}

type Adapter interface {
	Submit(context.Context, CanonicalRequest) (Submission, error)
	Poll(context.Context, Submission) (Result, error)
	Cancel(context.Context, Submission) (CancelResult, error)
	Probe(context.Context) Health
}

type Error struct {
	Code                string
	Message             string
	Retryable           bool
	SubmissionUncertain bool
	PauseProvider       bool
	RetryAfter          time.Duration
	Telemetry           Telemetry
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Message) }
