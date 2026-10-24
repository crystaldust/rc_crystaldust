package domain

import (
	"time"

	"github.com/google/uuid"
)

// Outcome classifies the result of one attempt (§7.3).
type Outcome string

const (
	OutcomeSuccess     Outcome = "success"
	OutcomeRetryable   Outcome = "retryable"
	OutcomePermanent   Outcome = "permanent"
	OutcomeRenderError Outcome = "render_error"
	OutcomeInfraError  Outcome = "infra_error"
)

// AttemptTrigger records whether an attempt came from normal scheduling or a replay.
type AttemptTrigger string

const (
	AttemptAuto   AttemptTrigger = "auto"
	AttemptReplay AttemptTrigger = "replay"
)

// MaxRecordedBodyBytes caps request and response bodies stored on an attempt (§10).
const MaxRecordedBodyBytes = 16 * 1024

// MaskedValue replaces secret-bearing header values in attempt records (§10).
const MaskedValue = "***"

// RecordedRequest is what the Hub sent, after masking and truncation.
type RecordedRequest struct {
	Method    string            `json:"method"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers"`
	Body      string            `json:"body"`
	Truncated bool              `json:"truncated"`
}

// RecordedResponse is what the vendor returned, after truncation.
type RecordedResponse struct {
	StatusCode  int    `json:"status_code"`
	ContentType string `json:"content_type"`
	Body        string `json:"body"`
	Truncated   bool   `json:"truncated"`
}

// Attempt is one HTTP call made for a Delivery. Attempts are append-only.
type Attempt struct {
	ID            uuid.UUID
	DeliveryID    uuid.UUID
	Number        int
	Trigger       AttemptTrigger
	StartedAt     time.Time
	DurationMS    int
	Outcome       Outcome
	StatusCode    *int
	Error         string
	Request       *RecordedRequest
	Response      *RecordedResponse
	ConfigHash    string
	NextAttemptAt *time.Time
}
