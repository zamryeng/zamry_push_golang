package fcm

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zamryeng/zamry_push_golang/push"
)

// vendor is a stand-in for both of the vendor's endpoints, so a test can assert
// on what actually went over the wire rather than on what the code meant to
// send.
type vendor struct {
	server *httptest.Server

	mints atomic.Int32
	sends atomic.Int32

	lastAssertion string
	lastSend      sendRequest
	lastAuth      string

	status int
	body   string

	membershipCalls      atomic.Int32
	lastMembership       membershipRequest
	lastMembershipOp     string
	lastMembershipHeader string
	membershipStatus     int

	// membershipResults is the per-token answer, positionally: an empty string
	// for a token the vendor accepted.
	membershipResults []string
}

func newVendor(t *testing.T) *vendor {
	t.Helper()
	v := &vendor{status: 200, body: `{"name":"projects/p/messages/1"}`}

	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		v.mints.Add(1)
		_ = r.ParseForm()
		v.lastAssertion = r.FormValue("assertion")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"access-xyz","expires_in":3600}`))
	})
	mux.HandleFunc("/v1/projects/", func(w http.ResponseWriter, r *http.Request) {
		v.sends.Add(1)
		v.lastAuth = r.Header.Get("Authorization")
		// Zeroed first. Decode merges into whatever is already there, and every
		// field on the wire is omitempty — so a field the client correctly
		// left out would read back as the previous request's value, and the
		// double would report a bug the client does not have.
		v.lastSend = sendRequest{}
		_ = json.NewDecoder(r.Body).Decode(&v.lastSend)
		w.WriteHeader(v.status)
		_, _ = w.Write([]byte(v.body))
	})

	mux.HandleFunc("/iid/v1:batchAdd", v.membership("batchAdd"))
	mux.HandleFunc("/iid/v1:batchRemove", v.membership("batchRemove"))

	v.server = httptest.NewServer(mux)
	t.Cleanup(v.server.Close)
	return v
}

func (v *vendor) membership(op string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v.membershipCalls.Add(1)
		v.lastMembershipOp = op
		v.lastMembershipHeader = r.Header.Get("access_token_auth")
		v.lastMembership = membershipRequest{}
		_ = json.NewDecoder(r.Body).Decode(&v.lastMembership)

		if v.membershipStatus != 0 && v.membershipStatus != 200 {
			w.WriteHeader(v.membershipStatus)
			_, _ = w.Write([]byte(`{"error":"nope"}`))
			return
		}

		results := v.membershipResults
		if results == nil {
			results = make([]string, len(v.lastMembership.Tokens))
		}
		var answer membershipResponse
		for _, e := range results {
			answer.Results = append(answer.Results, struct {
				Error string `json:"error"`
			}{Error: e})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(answer)
	}
}

func testClient(t *testing.T, v *vendor) *Client {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})

	c, err := New(Config{
		ProjectID:     "test-project",
		ClientEmail:   "push@test-project.iam.gserviceaccount.com",
		PrivateKeyPEM: string(keyPEM),
		SendHost:      v.server.URL,
		TokenHost:     v.server.URL,
		TopicHost:     v.server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func aMessage() push.Message {
	return push.Message{
		Title:     "Money in",
		Body:      "You received 5,000",
		Data:      map[string]string{"type": "transfer_received", "id": "42"},
		ChannelID: "transactions",
		Priority:  push.PriorityHigh,
		Lifetime:  2 * time.Hour,
	}
}

func TestSendDelivers(t *testing.T) {
	v := newVendor(t)
	c := testClient(t, v)

	out := c.Send(context.Background(), "tok-1", aMessage())

	if out.Kind != push.Delivered {
		t.Fatalf("got %v (%s), want Delivered", out.Kind, out.Reason)
	}
	if out.Token != "tok-1" {
		t.Errorf("Token = %q — the caller needs it back to retire the right row", out.Token)
	}
	if v.lastAuth != "Bearer access-xyz" {
		t.Errorf("Authorization = %q", v.lastAuth)
	}
}

func TestSendBuildsTheEnvelope(t *testing.T) {
	v := newVendor(t)
	c := testClient(t, v)

	c.Send(context.Background(), "tok-1", aMessage())
	m := v.lastSend.Message

	if m.Token != "tok-1" {
		t.Errorf("token = %q", m.Token)
	}
	if m.Notification == nil || m.Notification.Title != "Money in" {
		t.Errorf("notification = %+v", m.Notification)
	}
	if m.Data["id"] != "42" {
		t.Errorf("data = %v", m.Data)
	}
	if m.Android.Priority != "HIGH" {
		t.Errorf("android priority = %q — the vendor's enum is upper case, and the "+
			"lower-case spelling is refused as an invalid argument that looks like a "+
			"payload bug somewhere else", m.Android.Priority)
	}
	if m.Android.TTL != "7200s" {
		t.Errorf("ttl = %q, want whole seconds — a fractional one is refused as a malformed argument", m.Android.TTL)
	}
	if m.Android.Notification == nil || m.Android.Notification.ChannelID != "transactions" {
		t.Errorf("channel = %+v — without it every notification lands on one switch", m.Android.Notification)
	}
	if m.APNS.Headers["apns-push-type"] != "alert" {
		t.Errorf("apns push type = %q", m.APNS.Headers["apns-push-type"])
	}
	if m.APNS.Headers["apns-expiration"] == "" {
		t.Error("apns-expiration should be an absolute unix second")
	}
}

func TestSilentPushIsADifferentPushType(t *testing.T) {
	v := newVendor(t)
	c := testClient(t, v)

	silent := aMessage()
	silent.Silent = true
	silent.Title, silent.Body = "", ""

	c.Send(context.Background(), "tok-1", silent)
	m := v.lastSend.Message

	if m.Notification != nil {
		t.Errorf("silent push carried a notification block: %+v", m.Notification)
	}
	if got := m.APNS.Headers["apns-push-type"]; got != "background" {
		t.Errorf("apns-push-type = %q, want background", got)
	}
	if got := m.APNS.Headers["apns-priority"]; got != "5" {
		t.Errorf("apns-priority = %q — APNs refuses a background push at 10, and the "+
			"refusal reads as a payload bug", got)
	}
	if m.APNS.Payload.APS.ContentAvailable != 1 {
		t.Error("content-available must be 1 or iOS never wakes the app")
	}
	if m.APNS.Payload.APS.Sound != "" {
		t.Error("a silent push must not carry a sound")
	}
}

// The zero value of content-available must be absent, not 0: iOS reads the
// key's presence as the request, so an explicit zero asks for a background wake
// on every ordinary notification.
func TestOrdinaryPushOmitsContentAvailable(t *testing.T) {
	v := newVendor(t)
	c := testClient(t, v)

	c.Send(context.Background(), "tok-1", aMessage())
	raw, _ := json.Marshal(v.lastSend.Message.APNS.Payload)

	if strings.Contains(string(raw), "content-available") {
		t.Fatalf("ordinary push carried content-available: %s", raw)
	}
}

func TestAccessTokenIsCachedAcrossSends(t *testing.T) {
	v := newVendor(t)
	c := testClient(t, v)

	for range 5 {
		c.Send(context.Background(), "tok-1", aMessage())
	}

	if got := v.mints.Load(); got != 1 {
		t.Fatalf("minted %d tokens for 5 sends — the vendor rate-limits the token "+
			"endpoint far harder than the send endpoint", got)
	}
	if got := v.sends.Load(); got != 5 {
		t.Errorf("sent %d, want 5", got)
	}
}

func TestAccessTokenRefreshesBeforeExpiry(t *testing.T) {
	v := newVendor(t)
	c := testClient(t, v)

	now := time.Now()
	c.cfg.Now = func() time.Time { return now }

	c.Send(context.Background(), "tok-1", aMessage())
	if got := v.mints.Load(); got != 1 {
		t.Fatalf("mints = %d", got)
	}

	// Inside the hour but within the refresh margin.
	now = now.Add(59*time.Minute + 30*time.Second)
	c.Send(context.Background(), "tok-1", aMessage())

	if got := v.mints.Load(); got != 2 {
		t.Fatalf("mints = %d — a token that is still valid now and expired on arrival "+
			"must be replaced before it is used", got)
	}
}

func TestAssertionIsAWellFormedJWT(t *testing.T) {
	v := newVendor(t)
	c := testClient(t, v)

	c.Send(context.Background(), "tok-1", aMessage())

	parts := strings.Split(v.lastAssertion, ".")
	if len(parts) != 3 {
		t.Fatalf("assertion has %d segments, want 3", len(parts))
	}

	var header struct{ Alg, Typ string }
	seg, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("header is not base64url: %v", err)
	}
	if err := json.Unmarshal(seg, &header); err != nil {
		t.Fatal(err)
	}
	if header.Alg != "RS256" || header.Typ != "JWT" {
		t.Errorf("header = %+v", header)
	}

	var claims struct {
		Iss, Scope, Aud string
		Iat, Exp        int64
	}
	seg, _ = base64.RawURLEncoding.DecodeString(parts[1])
	if err := json.Unmarshal(seg, &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Iss != "push@test-project.iam.gserviceaccount.com" {
		t.Errorf("iss = %q", claims.Iss)
	}
	if claims.Scope != messagingScope {
		t.Errorf("scope = %q", claims.Scope)
	}
	if !strings.HasSuffix(claims.Aud, "/token") {
		t.Errorf("aud = %q — it must be the token endpoint itself", claims.Aud)
	}
	if claims.Exp <= claims.Iat {
		t.Errorf("exp %d is not after iat %d", claims.Exp, claims.Iat)
	}
}

func TestSendClassifiesAVendorRefusal(t *testing.T) {
	v := newVendor(t)
	c := testClient(t, v)
	v.status, v.body = 404, bodyUnregistered

	out := c.Send(context.Background(), "tok-dead", aMessage())

	if out.Kind != push.StaleToken {
		t.Fatalf("got %v, want StaleToken", out.Kind)
	}
	if out.Token != "tok-dead" {
		t.Errorf("Token = %q — the caller cannot retire a row it is not told about", out.Token)
	}
}

func TestEmptyTokenAbstains(t *testing.T) {
	v := newVendor(t)
	c := testClient(t, v)

	out := c.Send(context.Background(), "", aMessage())

	if out.Kind != push.Abstained {
		t.Fatalf("got %v, want Abstained", out.Kind)
	}
	if v.sends.Load() != 0 {
		t.Error("an empty token should not reach the vendor at all")
	}
}

func TestTokenEndpointFailureIsRetryable(t *testing.T) {
	v := newVendor(t)
	c := testClient(t, v)
	c.cfg.TokenHost = v.server.URL + "/nope"

	out := c.Send(context.Background(), "tok-1", aMessage())

	if out.Kind != push.Retryable {
		t.Fatalf("got %v (%s), want Retryable — nothing about the message is wrong", out.Kind, out.Reason)
	}
}

func TestDryRunSetsValidateOnly(t *testing.T) {
	v := newVendor(t)
	c := testClient(t, v)
	c.cfg.DryRun = true

	c.Send(context.Background(), "tok-1", aMessage())

	if !v.lastSend.ValidateOnly {
		t.Error("validate_only should be set, or a dry run wakes a handset")
	}
}

func TestNewRejectsIncompleteConfig(t *testing.T) {
	cases := map[string]Config{
		"no project":     {ClientEmail: "a@b", PrivateKeyPEM: "x"},
		"no email":       {ProjectID: "p", PrivateKeyPEM: "x"},
		"no key":         {ProjectID: "p", ClientEmail: "a@b"},
		"key is not pem": {ProjectID: "p", ClientEmail: "a@b", PrivateKeyPEM: `-----BEGIN PRIVATE KEY-----\nabc\n-----END PRIVATE KEY-----`},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := New(cfg); err == nil {
				t.Fatal("want an error at startup, where somebody is watching")
			}
		})
	}
}

func TestParseServiceAccount(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))

	file, _ := json.Marshal(map[string]string{
		"type":         "service_account",
		"project_id":   "zamry-push",
		"client_email": "push@zamry-push.iam.gserviceaccount.com",
		"private_key":  keyPEM,
	})

	cfg, err := ParseServiceAccount(file)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProjectID != "zamry-push" {
		t.Errorf("ProjectID = %q", cfg.ProjectID)
	}
	if _, err := New(cfg); err != nil {
		t.Fatalf("the parsed config should be usable as-is: %v", err)
	}

	if _, err := ParseServiceAccount([]byte(`{"type":"authorized_user"}`)); err == nil {
		t.Error("a user credential is not a service account and must be refused")
	}
}
