package api

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"messagehub/internal/domain"
	"messagehub/internal/store"
)

func TestOperatorActions(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		wantStatus int
	}{
		{"replay answers 202", "replay", http.StatusAccepted},
		{"skip answers 200", "skip", http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			h := newHarness(t)
			id := uuid.New()
			h.store.action = store.OperatorAction{ID: domain.NewID(), Action: store.ActionReplay, DeliveryID: id,
				Actor: "oncall-operator", Reason: "fixed", FromStatus: domain.StatusDead, ToStatus: domain.StatusPending, CreatedAt: time.Now()}

			// Act
			rec, body := h.do(t, http.MethodPost, "/v1/deliveries/"+id.String()+"/"+tt.path, "oncall-operator", `{"reason":"fixed"}`)

			// Assert
			if rec.Code != tt.wantStatus {
				t.Fatalf("status %d, body %v", rec.Code, body)
			}
			in := h.store.operatorIn
			if in.DeliveryID != id || in.Actor != "oncall-operator" || in.Reason != "fixed" || in.ActionID == uuid.Nil {
				t.Errorf("operator input = %+v", in)
			}
			data := body["data"].(map[string]any)
			if data["action_id"] == nil || data["from_status"] != "DEAD" || data["to_status"] != "PENDING" {
				t.Errorf("data = %v", data)
			}
			if !strings.Contains(h.logs.String(), `"msg":"delivery.operator_action"`) {
				t.Error("operator action not logged")
			}
		})
	}
}

func TestOperatorErrors(t *testing.T) {
	id := uuid.NewString()
	tests := []struct {
		name       string
		principal  string
		target     string
		body       string
		storeErr   error
		wantStatus int
		wantCode   string
		wantFields []string
	}{
		{"viewer may not replay", "support-viewer", "/v1/deliveries/" + id + "/replay", `{"reason":"x"}`, nil, http.StatusForbidden, codeForbidden, nil},
		{"source may not skip", "order-service", "/v1/deliveries/" + id + "/skip", `{"reason":"x"}`, nil, http.StatusForbidden, codeForbidden, nil},
		{"invalid state", "oncall-operator", "/v1/deliveries/" + id + "/skip", `{"reason":"x"}`,
			&store.StateError{Action: "skip", Current: domain.StatusInFlight}, http.StatusConflict, codeInvalidState, []string{"status"}},
		{"unknown delivery", "oncall-operator", "/v1/deliveries/" + id + "/replay", `{"reason":"x"}`, store.ErrNotFound, http.StatusNotFound, codeNotFound, nil},
		{"missing reason and bad id", "oncall-operator", "/v1/deliveries/42/replay", `{}`, nil, http.StatusBadRequest, codeValidationFailed, []string{"delivery_id", "reason"}},
		{"reason too long", "oncall-operator", "/v1/deliveries/" + id + "/replay", `{"reason":"` + strings.Repeat("r", maxReasonLen+1) + `"}`, nil, http.StatusBadRequest, codeValidationFailed, []string{"reason"}},
		{"reason not a string", "oncall-operator", "/v1/deliveries/" + id + "/replay", `{"reason":null}`, nil, http.StatusBadRequest, codeValidationFailed, []string{"reason"}},
		{"unknown field", "oncall-operator", "/v1/deliveries/" + id + "/replay", `{"reason":"x","force":true}`, nil, http.StatusBadRequest, codeValidationFailed, []string{"force"}},
		{"duplicate key", "oncall-operator", "/v1/deliveries/" + id + "/replay", `{"reason":"x","reason":"y"}`, nil, http.StatusBadRequest, codeValidationFailed, []string{"reason"}},
		{"NUL in reason", "oncall-operator", "/v1/deliveries/" + id + "/replay", `{"reason":"a\u0000"}`, nil, http.StatusBadRequest, codeValidationFailed, []string{"reason"}},
		{"not an object", "oncall-operator", "/v1/deliveries/" + id + "/replay", `[]`, nil, http.StatusBadRequest, codeValidationFailed, []string{""}},
		{"body too large", "oncall-operator", "/v1/deliveries/" + id + "/replay", strings.Repeat(" ", maxOperatorBodyBytes+1), nil, http.StatusRequestEntityTooLarge, codePayloadTooLarge, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.store.err = tt.storeErr

			rec, body := h.do(t, http.MethodPost, tt.target, tt.principal, tt.body)

			if rec.Code != tt.wantStatus || errorCode(body) != tt.wantCode {
				t.Fatalf("status %d code %q, want %d %q (%v)", rec.Code, errorCode(body), tt.wantStatus, tt.wantCode, body)
			}
			for _, f := range tt.wantFields {
				if !slices.Contains(errorFields(body), f) {
					t.Errorf("details %v do not mention %q", errorFields(body), f)
				}
			}
		})
	}
}
