package fcm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/zamryeng/zamry_push_golang/push"
)

const defaultTopicHost = "https://iid.googleapis.com"

// maxTopicBatch is the most tokens the vendor accepts in one membership call.
const maxTopicBatch = 1000

var (
	_ push.Broadcaster     = (*Client)(nil)
	_ push.TopicMembership = (*Client)(nil)
	_ push.Broadcaster     = (*Console)(nil)
	_ push.TopicMembership = (*Console)(nil)
)

// Broadcast delivers an announcement to a topic.
//
// A successful return means the vendor accepted the message and nothing more:
// topic delivery is optimised for throughput over latency and reports no
// per-recipient result. There is deliberately no token on the outcome, because
// there is no row a failure could be attributed to.
func (c *Client) Broadcast(ctx context.Context, t push.Topic, a push.Announcement) push.Outcome {
	if !t.Declared() {
		return push.Outcome{Kind: push.Permanent, Reason: "a broadcast needs a declared topic"}
	}
	if a.Title == "" || a.Body == "" {
		return push.Outcome{Kind: push.Permanent, Reason: "an announcement needs a title and a body"}
	}
	// Refused rather than defaulted, for the same reason Priority counts from
	// one: a notification that goes out at the wrong urgency, or with no
	// lifetime at all, is a failure no test notices and no log records.
	if !a.Priority.Valid() {
		return push.Outcome{Kind: push.Permanent, Reason: "an announcement needs a priority"}
	}
	if a.Lifetime <= 0 {
		return push.Outcome{Kind: push.Permanent, Reason: "an announcement needs a lifetime"}
	}
	// Refused at the vendor too, not only at the send site. A host may hold a
	// Broadcaster directly, and a topic anyone may join is the wrong place to
	// take a caller's word for it.
	if a.Link != "" && !push.InAppLink(a.Link) {
		return push.Outcome{Kind: push.Permanent, Reason: "an announcement's link must be an in-app path"}
	}

	m := push.Message{
		Title:     a.Title,
		Body:      a.Body,
		ChannelID: a.ChannelID,
		Priority:  a.Priority,
		Lifetime:  a.Lifetime,
		Private:   a.Private,
	}
	if a.Link != "" {
		// The only thing an announcement's data may carry. An in-app path, not
		// a URL: a client that opened it as a URL would turn a channel anyone
		// may subscribe to into a way of opening arbitrary pages on a
		// stranger's handset.
		m.Data = map[string]string{"link": a.Link}
	}

	v := c.envelope("", m)
	v.Topic = t.String()
	return notAboutAToken(c.postMessage(ctx, v))
}

// BroadcastSignal delivers data to a topic and shows nothing.
func (c *Client) BroadcastSignal(ctx context.Context, t push.Topic, s push.Signal) push.Outcome {
	if !t.Declared() {
		return push.Outcome{Kind: push.Permanent, Reason: "a signal needs a declared topic"}
	}
	if len(s.Data) == 0 {
		return push.Outcome{Kind: push.Permanent, Reason: push.ErrEmptySignal.Error()}
	}
	if s.Lifetime <= 0 {
		// The sibling of the empty-data check. A signal says something changed
		// *now*, so one the vendor keeps trying to deliver forever wakes an app
		// for news it would have found on its own.
		return push.Outcome{Kind: push.Permanent, Reason: "a signal needs a lifetime"}
	}

	v := c.envelope("", push.Message{
		Data: s.Data, Lifetime: s.Lifetime, Silent: true,
		// Silent sends fix their own priority on both platforms, so this is
		// only here to pass the envelope's validity check.
		Priority: push.PriorityHigh,
	})
	v.Topic = t.String()
	return notAboutAToken(c.postMessage(ctx, v))
}

// postMessage sends one envelope and classifies the answer.
func (c *Client) postMessage(ctx context.Context, v vendorMessage) push.Outcome {
	access, err := c.accessToken(ctx)
	if err != nil {
		return push.Outcome{Kind: push.Retryable, Reason: "minting an access token: " + err.Error(), Err: err}
	}

	body, err := json.Marshal(sendRequest{ValidateOnly: c.cfg.DryRun, Message: v})
	if err != nil {
		return push.Outcome{Kind: push.Permanent, Reason: "encoding the message: " + err.Error(), Err: err}
	}

	endpoint := c.cfg.SendHost + "/v1/projects/" + url.PathEscape(c.cfg.ProjectID) + "/messages:send"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return push.Outcome{Kind: push.Permanent, Reason: err.Error(), Err: err}
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Content-Type", "application/json")

	status, respBody, err := c.do(req)
	if status == http.StatusUnauthorized {
		c.forgetAccessToken()
	}

	return classify(status, respBody, err)
}

// notAboutAToken rewrites a stale-token answer for a path that sent no token.
//
// A topic send has no token, so the vendor cannot be talking about one, and
// reporting StaleToken would invite a caller to retire whichever row happened
// to be nearby. It lives here rather than in postMessage because postMessage is
// shared with the per-device path — where a stale token is exactly what it says
// and rewriting it would mean no dead row is ever retired.
func notAboutAToken(out push.Outcome) push.Outcome {
	if out.Kind == push.StaleToken {
		out.Kind = push.Permanent
		out.Reason = "the vendor refused a topic send as it would a stale token: " + out.Reason
	}
	out.Token = ""
	return out
}

// Subscribe adds tokens to a topic, and answers the ones the vendor refused.
func (c *Client) Subscribe(ctx context.Context, t push.Topic, tokens []string) ([]string, error) {
	return c.membership(ctx, "batchAdd", t, tokens)
}

// Unsubscribe removes tokens from a topic.
func (c *Client) Unsubscribe(ctx context.Context, t push.Topic, tokens []string) ([]string, error) {
	return c.membership(ctx, "batchRemove", t, tokens)
}

// membership runs one membership operation, splitting a long list.
//
// The vendor takes at most a thousand tokens per call, and a caller setting a
// fresh deployment's topics will hand over more than that. Splitting here
// rather than at the caller means nobody discovers the limit from a refusal.
func (c *Client) membership(ctx context.Context, op string, t push.Topic, tokens []string) ([]string, error) {
	if !t.Declared() {
		return nil, fmt.Errorf("%w: a membership change needs a declared topic", push.ErrMalformed)
	}
	for _, tok := range tokens {
		if tok == "" {
			return nil, fmt.Errorf("%w: a membership change was given an empty token", push.ErrMalformed)
		}
	}
	if len(tokens) == 0 {
		return nil, nil
	}

	access, err := c.accessToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("fcm: minting an access token: %w", err)
	}

	var refused []string
	for _, batch := range batches(tokens) {
		batchRefused, err := c.membershipBatch(ctx, op, access, t, batch)
		if err != nil {
			// Partial progress is reported alongside the error: the batches
			// that succeeded really did, and a caller retrying the whole list
			// is harmless because membership is idempotent.
			return refused, err
		}
		refused = append(refused, batchRefused...)
	}
	return refused, nil
}

func (c *Client) membershipBatch(ctx context.Context, op, access string, t push.Topic, tokens []string) ([]string, error) {
	body, err := json.Marshal(membershipRequest{To: "/topics/" + t.String(), Tokens: tokens})
	if err != nil {
		return nil, err
	}

	endpoint := c.cfg.TopicHost + "/iid/v1:" + op
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Content-Type", "application/json")
	// Required by this endpoint specifically, and omitting it is a 400 that
	// says nothing about the header.
	req.Header.Set("access_token_auth", "true")

	status, respBody, transportErr := c.do(req)
	if transportErr != nil {
		return nil, fmt.Errorf("fcm: %s: %w", op, transportErr)
	}
	if status == http.StatusUnauthorized {
		c.forgetAccessToken()
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("fcm: %s: %s", op, vendorReason(status, respBody))
	}

	var answer membershipResponse
	if err := json.Unmarshal([]byte(respBody), &answer); err != nil {
		// A 2xx the body of which cannot be read means every token in the
		// batch was accepted as far as anyone can tell; saying otherwise would
		// invent refusals.
		return nil, nil
	}

	// The vendor answers positionally, one result per token sent. A different
	// count means the positions cannot be trusted, and guessing either way is
	// worse than retrying: calling a good token refused gets it retired, and
	// calling a refused one accepted leaves a handset silently off the topic.
	// Membership is idempotent, so the caller can simply run it again.
	if len(answer.Results) != len(tokens) {
		return nil, fmt.Errorf("fcm: %s: the vendor answered %d results for %d tokens",
			op, len(answer.Results), len(tokens))
	}

	var refused []string
	for i, r := range answer.Results {
		if r.Error != "" {
			refused = append(refused, tokens[i])
		}
	}
	return refused, nil
}

// batches splits tokens into the vendor's largest accepted size.
func batches(tokens []string) [][]string {
	var out [][]string
	for len(tokens) > maxTopicBatch {
		out = append(out, tokens[:maxTopicBatch])
		tokens = tokens[maxTopicBatch:]
	}
	if len(tokens) > 0 {
		out = append(out, tokens)
	}
	return out
}
