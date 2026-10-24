package worker

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"
	"unicode/utf8"

	"messagehub/internal/backoff"
	"messagehub/internal/config"
	"messagehub/internal/dispatch"
	"messagehub/internal/domain"
	"messagehub/internal/outbound"
	"messagehub/internal/outcome"
	"messagehub/internal/secrets"
	"messagehub/internal/template"
)

const (
	// maxErrorBytes bounds the error text stored on attempts and deliveries.
	maxErrorBytes = 1024
	// completeAttempts and completeRetryDelay (doubling) bound retries of
	// a failed result write.
	completeAttempts   = 3
	completeRetryDelay = 100 * time.Millisecond
)

// try is everything learnt during one attempt, before it is classified.
type try struct {
	claimed    dispatch.Claimed
	configHash string
	policy     config.Policy
	method     string
	number     int
	started    time.Time
	duration   time.Duration
	rendered   *template.Rendered
	resp       *outbound.Response
	in         outcome.Input
	errText    string
}

func (t try) withInfraErr(err error) try {
	t.in.InfraErr, t.errText = err, err.Error()
	return t
}

func (t try) withRenderErr(err error) try {
	t.in.RenderErr, t.errText = err, err.Error()
	return t
}

// Process runs the full pipeline for one claimed delivery (§7) and
// completes it: config snapshot → event → secrets → render → auth → send →
// classify → backoff → transition and attempt record in one transaction.
func (w *Worker) Process(ctx context.Context, c dispatch.Claimed) {
	cfg := w.config() // one snapshot per delivery, never cached
	dest, policy, ok := target(cfg, c.DestinationID)
	var comp dispatch.Completion
	switch {
	case !ok:
		comp = w.missingConfig(c, cfg.Hash())
	case c.Reclaimed && c.AttemptCount >= policy.Retry.MaxAttempts:
		comp = w.budgetSpent(c)
	default:
		comp = w.decide(w.attempt(ctx, c, cfg.Hash(), dest, policy))
	}
	w.complete(ctx, comp)
}

func target(cfg *config.Config, destinationID string) (config.Destination, config.Policy, bool) {
	dest, ok := cfg.Destination(destinationID)
	if !ok {
		return config.Destination{}, config.Policy{}, false
	}
	policy, ok := cfg.Policy(dest.Policy)
	return dest, policy, ok
}

func (w *Worker) attempt(ctx context.Context, c dispatch.Claimed, configHash string, dest config.Destination, policy config.Policy) try {
	t := try{claimed: c, configHash: configHash, policy: policy, method: dest.Request.Method,
		number: c.AttemptCount + 1, started: w.now()}
	ev, err := w.events.LoadEvent(ctx, c.EventID)
	if err != nil {
		return t.withInfraErr(err)
	}
	vals, err := resolveSecrets(w.secrets, dest.Secrets)
	if err != nil {
		return t.withInfraErr(err)
	}
	rendered, err := template.Render(dest.Request, template.Vars{
		Event: ev, Secrets: vals, DeliveryID: c.DeliveryID.String(), Attempt: t.number, Now: t.started,
	})
	if err != nil {
		return t.withRenderErr(err)
	}
	authz, err := w.tokens.Authorization(dest.ID, configHash, dest.Auth)
	if err != nil {
		return t.withInfraErr(err) // nothing was sent, so no request is recorded
	}
	rendered = withAuthorization(rendered, authz)
	t.rendered = &rendered
	return w.send(ctx, t)
}

func (w *Worker) send(ctx context.Context, t try) try {
	req := outbound.Request{Method: t.method, URL: t.rendered.Actual.URL,
		Headers: t.rendered.Actual.Headers, Body: t.rendered.Actual.BodyBytes}
	resp, err := w.client.Send(ctx, req, t.policy.Timeout.Std())
	t.duration = w.now().Sub(t.started)
	switch {
	case errors.Is(err, outbound.ErrInvalidRequest):
		return t.withRenderErr(err)
	case errors.Is(err, outbound.ErrInfra):
		return t.withInfraErr(err)
	case err != nil:
		t.in.NetErr, t.errText = err, err.Error()
		return t
	}
	t.resp = &resp
	t.in = outcome.Input{StatusCode: resp.StatusCode, ContentType: resp.ContentType,
		Body: resp.Body, BodyComplete: resp.BodyComplete}
	return t
}

func resolveSecrets(r secrets.Resolver, declared map[string]string) (map[string]string, error) {
	vals := make(map[string]string, len(declared))
	for name, ref := range declared {
		v, err := r.Resolve(ref)
		if err != nil {
			return nil, fmt.Errorf("%w: secret %s: %w", outbound.ErrInfra, name, err)
		}
		vals[name] = v
	}
	return vals, nil
}

// withAuthorization adds the OAuth2 header to copies of both header maps,
// replacing a configured Authorization in any letter case; the record
// masks it by name anyway (§10).
func withAuthorization(r template.Rendered, value string) template.Rendered {
	if value == "" {
		return r
	}
	isAuth := func(name, _ string) bool { return strings.EqualFold(name, outbound.AuthorizationHeader) }
	r.Actual.Headers = maps.Clone(r.Actual.Headers)
	r.Masked.Headers = maps.Clone(r.Masked.Headers)
	maps.DeleteFunc(r.Actual.Headers, isAuth)
	maps.DeleteFunc(r.Masked.Headers, isAuth)
	r.Actual.Headers[outbound.AuthorizationHeader] = value
	r.Masked.Headers[outbound.AuthorizationHeader] = domain.MaskedValue
	return r
}

// decide maps the outcome to a trigger (§7.3) and builds the completion.
func (w *Worker) decide(t try) dispatch.Completion {
	oc := outcome.Classify(t.in, t.policy)
	comp := dispatch.Completion{
		Claimed:       t.claimed,
		Outcome:       oc,
		Error:         truncateText(t.errorText(oc), maxErrorBytes),
		AttemptedAt:   t.started,
		CountsAttempt: wasSent(oc),
	}
	if t.resp != nil {
		code := t.resp.StatusCode
		comp.StatusCode = &code
	}
	comp.Trigger, comp.RetryDelay = w.next(t, oc)
	comp.Attempt = t.record(comp)
	return comp
}

// wasSent reports whether the request reached, or may have reached, the
// vendor; only then does the attempt consume budget (§5.3).
func wasSent(oc domain.Outcome) bool {
	return oc == domain.OutcomeSuccess || oc == domain.OutcomeRetryable || oc == domain.OutcomePermanent
}

func (w *Worker) next(t try, oc domain.Outcome) (domain.Trigger, *time.Duration) {
	switch oc {
	case domain.OutcomeSuccess:
		return domain.TriggerSucceed, nil
	case domain.OutcomeRetryable:
		if t.number < t.policy.Retry.MaxAttempts {
			return domain.TriggerRetry, w.retryDelay(t)
		}
		return domain.TriggerGiveUp, nil
	case domain.OutcomeInfraError:
		return domain.TriggerInfraRetry, w.retryDelay(t)
	default:
		return domain.TriggerGiveUp, nil
	}
}

// retryDelay is the wait before the next attempt (§7.4); the dispatcher
// adds it to the database clock. Infrastructure retries use the backoff
// at the current retry number, since they do not advance the count.
func (w *Worker) retryDelay(t try) *time.Duration {
	status, retryAfter := 0, ""
	if t.resp != nil {
		status, retryAfter = t.resp.StatusCode, t.resp.RetryAfter
	}
	d := backoff.Delay(t.policy.Retry.Backoff, t.number, status, retryAfter, w.now(), w.rnd)
	return &d
}

func (t try) errorText(oc domain.Outcome) string {
	switch {
	case t.errText != "":
		return t.errText
	case t.resp != nil && oc != domain.OutcomeSuccess:
		return fmt.Sprintf("HTTP %d did not meet the success criteria", t.resp.StatusCode)
	default:
		return ""
	}
}

func (t try) record(comp dispatch.Completion) *domain.Attempt {
	a := domain.Attempt{
		ID:         domain.NewID(),
		DeliveryID: t.claimed.DeliveryID,
		Number:     t.number,
		Trigger:    dispatch.AttemptTrigger(t.claimed.ReplayCount),
		StartedAt:  t.started,
		DurationMS: int(t.duration.Milliseconds()),
		Outcome:    comp.Outcome,
		StatusCode: comp.StatusCode,
		Error:      comp.Error,
		ConfigHash: t.configHash,
	}
	if t.rendered != nil {
		req := outbound.BuildRequestRecord(*t.rendered, t.method)
		a.Request = &req
	}
	if t.resp != nil {
		resp := outbound.BuildResponseRecord(t.resp.StatusCode, t.resp.ContentType, t.resp.Body)
		a.Response = &resp
	}
	return &a
}

// missingConfig gives up on a delivery whose destination or policy left the
// config between claim and processing; replay it once the config is fixed.
func (w *Worker) missingConfig(c dispatch.Claimed, configHash string) dispatch.Completion {
	now := w.now()
	msg := fmt.Sprintf("destination %s or its policy is not in the current config", c.DestinationID)
	return dispatch.Completion{
		Claimed: c, Trigger: domain.TriggerGiveUp, Outcome: domain.OutcomePermanent, Error: msg,
		AttemptedAt: now,
		Attempt: &domain.Attempt{
			ID: domain.NewID(), DeliveryID: c.DeliveryID, Number: c.AttemptCount + 1,
			Trigger: dispatch.AttemptTrigger(c.ReplayCount), StartedAt: now,
			Outcome: domain.OutcomePermanent, Error: msg, ConfigHash: configHash,
		},
	}
}

// budgetSpent gives up on a reclaimed delivery whose lost attempt was the
// last one the policy allows; the reclaim already recorded that attempt.
func (w *Worker) budgetSpent(c dispatch.Claimed) dispatch.Completion {
	return dispatch.Completion{
		Claimed: c, Trigger: domain.TriggerGiveUp, Outcome: domain.OutcomeRetryable,
		Error: "retry budget exhausted: " + dispatch.ReclaimError, AttemptedAt: w.now(),
	}
}

// complete logs the attempt as one wide event and hands the result to the
// dispatcher. A failed write leaves the delivery IN_FLIGHT; its lease then
// expires and it is reclaimed.
func (w *Worker) complete(ctx context.Context, comp dispatch.Completion) {
	c := comp.Claimed
	attrs := []any{
		"event_id", c.EventID, "delivery_id", c.DeliveryID, "destination_id", c.DestinationID,
		"trigger", comp.Trigger, "outcome", comp.Outcome, "reclaimed", c.Reclaimed,
	}
	if a := comp.Attempt; a != nil {
		attrs = append(attrs, "attempt", a.Number, "attempt_trigger", a.Trigger,
			"duration_ms", a.DurationMS, "config_hash", a.ConfigHash)
	}
	if comp.StatusCode != nil {
		attrs = append(attrs, "status_code", *comp.StatusCode)
	}
	if comp.RetryDelay != nil {
		attrs = append(attrs, "retry_in_ms", comp.RetryDelay.Milliseconds())
	}
	w.log.Info("delivery.attempted", attrs...)

	applied, err := w.completeWithRetry(ctx, comp)
	switch {
	case err != nil:
		w.log.Error("delivery.complete_failed", "delivery_id", c.DeliveryID, "error", err)
	case !applied:
		w.log.Warn("delivery.lease_lost", "delivery_id", c.DeliveryID, "destination_id", c.DestinationID)
	}
}

// completeWithRetry retries a failed write a few times: giving up leaves the
// delivery to be reclaimed, which charges the budget again and re-sends a
// request the vendor already received. Retrying is safe because the
// attempt keeps its id: a write that did commit makes the retry fail on the
// primary key instead of recording the attempt twice.
func (w *Worker) completeWithRetry(ctx context.Context, comp dispatch.Completion) (bool, error) {
	var err error
	for i := range completeAttempts {
		var applied bool
		if applied, err = w.dispatcher.Complete(ctx, comp); err == nil {
			return applied, nil
		}
		if i == completeAttempts-1 {
			break
		}
		select {
		case <-ctx.Done():
			return false, err
		case <-time.After(completeRetryDelay << i):
		}
	}
	return false, err
}

// truncateText makes error text storable in a text column (valid UTF-8, no
// NUL: parts of it, such as an OAuth2 error code, come from outside, and a
// rejected write would leave the delivery stuck IN_FLIGHT) and cuts it to n
// bytes on a character boundary.
func truncateText(s string, n int) string {
	s = strings.ReplaceAll(strings.ToValidUTF8(s, string(utf8.RuneError)), "\x00", string(utf8.RuneError))
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 { // do not split a UTF-8 sequence
		n--
	}
	return s[:n]
}
