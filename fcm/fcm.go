// Package fcm sends a [push.Message] to a handset through Firebase Cloud
// Messaging's HTTP v1 API.
//
// It depends on nothing outside the standard library, deliberately: a package
// consumed by git ref into several products should not drag a dependency tree
// behind it, and the two things a vendor SDK would do here — sign a JWT and
// make one POST — are forty lines of crypto/rsa and net/http.
//
// The valuable part is not the request. It is [classify]: which vendor answers
// mean the address is dead, which mean try again, and which mean the message
// itself is wrong. Those distinctions were learned from production traffic, and
// the comments in classify.go say what each one cost to find out.
package fcm

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zamryeng/zamry_push_golang/push"
)

const (
	defaultSendHost  = "https://fcm.googleapis.com"
	defaultTokenHost = "https://oauth2.googleapis.com"
	messagingScope   = "https://www.googleapis.com/auth/firebase.messaging"

	// How long before expiry a cached access token is replaced. The vendor
	// issues them for an hour; a minute of slack covers a send that starts
	// valid and arrives expired.
	refreshBefore = time.Minute
)

// Config is what the vendor needs to be talked to.
//
// The three required fields are exactly the three fields of a service-account
// key file that matter, which is why [ParseServiceAccount] exists: a host reads
// its own secret however it already reads secrets, and hands the bytes over.
type Config struct {
	ProjectID   string
	ClientEmail string

	// PrivateKeyPEM is the PKCS#8 block from the service-account key, newlines
	// and all. A key file's JSON escaping must be undone first — a literal
	// \n in the string is the most common cause of "failed to parse".
	PrivateKeyPEM string

	// HTTP is optional. The default has a thirty-second timeout, because the
	// zero value of http.Client has none at all and a hung connection would
	// hold a queue worker forever.
	HTTP *http.Client

	// SendHost, TokenHost and TopicHost are for tests and for a vendor
	// emulator. Empty means the real thing.
	//
	// TopicHost is a different service from the other two: topic membership
	// still lives on the old instance-id endpoint, which is why it is a third
	// host rather than another path.
	SendHost  string
	TokenHost string
	TopicHost string

	// DryRun asks the vendor to validate without delivering. Useful once, to
	// prove credentials and payload shape against the real endpoint without
	// waking anybody's handset.
	DryRun bool

	// Now is overridable for tests.
	Now func() time.Time
}

// Client sends messages. Safe for concurrent use, and intended to be built once
// per process: it caches the access token, and a fresh client per send would
// mint a new one every time.
type Client struct {
	cfg Config
	key *rsa.PrivateKey

	mu      sync.Mutex
	token   string
	expires time.Time
}

// New validates the configuration and parses the key.
//
// The key is parsed here rather than at first send so a typo in a deployment's
// secret fails at startup, where somebody is watching, instead of at 3am on the
// first notification.
func New(cfg Config) (*Client, error) {
	switch {
	case cfg.ProjectID == "":
		return nil, errors.New("fcm: ProjectID is required")
	case cfg.ClientEmail == "":
		return nil, errors.New("fcm: ClientEmail is required")
	case cfg.PrivateKeyPEM == "":
		return nil, errors.New("fcm: PrivateKeyPEM is required")
	}

	key, err := parseKey(cfg.PrivateKeyPEM)
	if err != nil {
		return nil, err
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: 30 * time.Second}
	}
	if cfg.SendHost == "" {
		cfg.SendHost = defaultSendHost
	}
	if cfg.TokenHost == "" {
		cfg.TokenHost = defaultTokenHost
	}
	if cfg.TopicHost == "" {
		cfg.TopicHost = defaultTopicHost
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Client{cfg: cfg, key: key}, nil
}

// Both implementations satisfy the send path's vendor seam. Asserted here
// because nothing else in this package references push.Vendor: a signature
// drifting apart from it would otherwise surface as a host's wiring failing to
// compile rather than as a test here.
var (
	_ push.Vendor = (*Client)(nil)
	_ push.Vendor = (*Console)(nil)
)

// ParseServiceAccount reads the three fields that matter out of a
// service-account key file.
//
// It does not read the file: a host's secrets come from wherever that host keeps
// secrets, and a library that opens paths invites one that is checked into a
// repository.
func ParseServiceAccount(b []byte) (Config, error) {
	var f struct {
		Type        string `json:"type"`
		ProjectID   string `json:"project_id"`
		ClientEmail string `json:"client_email"`
		PrivateKey  string `json:"private_key"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return Config{}, fmt.Errorf("fcm: service account is not JSON: %w", err)
	}
	if f.Type != "service_account" {
		return Config{}, fmt.Errorf("fcm: expected a service_account key, got %q", f.Type)
	}
	return Config{
		ProjectID:     f.ProjectID,
		ClientEmail:   f.ClientEmail,
		PrivateKeyPEM: f.PrivateKey,
	}, nil
}

// Send delivers one message to one token.
//
// It returns an outcome rather than an error, because three of the five
// possibilities are not errors: the caller needs to know whether to retry,
// whether to retire the row, or whether to stop — and an error value makes all
// three look the same.
func (c *Client) Send(ctx context.Context, token string, m push.Message) push.Outcome {
	if out, refused := refuseSend(token, m); refused {
		return out
	}

	out := c.postMessage(ctx, c.envelope(token, m))
	out.Token = token
	return out
}

// MaxLifetime is the longest ttl this vendor accepts.
//
// Four weeks. Above it the send is refused outright, so a kind declaring
// "a month" — which reads entirely reasonable — would never be delivered at
// all. Exported so a host can assert its own declarations against it at boot
// rather than discovering it one dropped notification at a time.
const MaxLifetime = 4 * 7 * 24 * time.Hour

// refuseSend is every check that happens before the wire.
//
// Shared by the real client and the console double deliberately. These drifted:
// the client refused six things and the double accepted all of them and
// reported Delivered, so a host's whole local run — console vendor, memory
// store, memory queue — validated nothing that production validates, and a
// blank title shipped green and died in staging.
//
// An outcome rather than an error because the distinction is the point: an
// empty token is an abstention, the rest are permanent.
func refuseSend(token string, m push.Message) (push.Outcome, bool) {
	switch {
	case token == "":
		return push.Outcome{Kind: push.Abstained, Reason: push.AbstainNoTokens}, true

	case !m.Silent && (m.Title == "" || m.Body == ""):
		return permanent(token, "a message needs a title and a body"), true

	case m.Silent && len(m.Data) == 0:
		return permanent(token, push.ErrEmptySignal.Error()), true

	case m.Silent && (m.Title != "" || m.Body != ""):
		// Refused rather than quietly dropped. The envelope ignores words on a
		// silent send, so without this a caller who set both would see a
		// delivered outcome and no notification, with nothing anywhere saying
		// why. [push.Signal], which the topic path uses, has no field to put a
		// title in — this is the per-device path catching up to that.
		return permanent(token, "a silent message cannot carry words"), true

	case !m.Priority.Valid():
		return permanent(token, "unknown priority"), true

	case m.Lifetime <= 0:
		return permanent(token, "a message needs a lifetime"), true

	case m.Lifetime > MaxLifetime:
		return permanent(token, "a lifetime longer than this vendor's four weeks"), true
	}
	return push.Outcome{}, false
}

func permanent(token, reason string) push.Outcome {
	return push.Outcome{Kind: push.Permanent, Token: token, Reason: reason}
}

// Warm obtains the access token before the first notification needs it.
//
// Worth calling at boot. Otherwise the first real send of a deployment pays for
// a token exchange on top of its own latency, and if the credential is wrong
// nobody finds out until something was meant to be delivered.
func (c *Client) Warm(ctx context.Context) error {
	_, err := c.accessToken(ctx)
	return err
}

// forgetAccessToken drops the cached token so the next call mints a new one.
func (c *Client) forgetAccessToken() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token, c.expires = "", time.Time{}
}

// do performs the request and reads the whole body.
//
// The body is read in full even on success, and capped: an error body carries
// the diagnosis, and an unbounded read of a vendor response is somebody else's
// memory limit.
func (c *Client) do(req *http.Request) (int, string, error) {
	resp, err := c.cfg.HTTP.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()

	const maxBody = 16 << 10
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		// A status arrived, so this is a truncated body rather than a failed
		// request. Classify on the status alone.
		return resp.StatusCode, "", nil
	}
	return resp.StatusCode, string(b), nil
}

// envelope builds the vendor's message shape from ours.
//
// Every platform-specific decision in the system is in this function and the
// two beside it, which is the point of [push.Message] carrying none of them.
func (c *Client) envelope(token string, m push.Message) vendorMessage {
	v := vendorMessage{Token: token, Data: m.Data}
	if !m.Silent {
		v.Notification = &vendorNotification{Title: m.Title, Body: m.Body, Image: m.Image}
	}
	v.Android = c.android(m)
	v.APNS = c.apns(m)
	return v
}

// android translates priority, lifetime, channel, replacement and privacy.
func (c *Client) android(m push.Message) *androidConfig {
	cfg := &androidConfig{Priority: androidPriority(m.Priority)}

	if m.Silent {
		// Always the high one, and not because a signal is urgent: below it
		// the platform holds a data message until the handset next leaves
		// Doze, which on an idle device is the difference between minutes and
		// hours — and a signal delivered hours late is one the app had already
		// acted on by itself.
		cfg.Priority = "HIGH"
	}

	if m.Lifetime > 0 {
		// Whole seconds: the vendor's ttl grammar is a duration string, and a
		// fractional one is rejected as a malformed argument.
		cfg.TTL = strconv.FormatInt(int64(m.Lifetime/time.Second), 10) + "s"
	}

	// No notification block at all when silent — a channel id here would file
	// it for a display that is not going to happen.
	if !m.Silent {
		cfg.Notification = &androidNotification{
			ChannelID:  m.ChannelID,
			Tag:        m.ReplaceID,
			Visibility: visibility(m.Private),
		}
	}
	return cfg
}

// androidPriority spells the vendor's enum.
//
// Upper case, because that is what the enum is. The lower-case spelling is
// refused as an invalid argument, which arrives looking like a payload bug
// somewhere else entirely.
func androidPriority(p push.Priority) string {
	if p == push.PriorityHigh {
		return "HIGH"
	}
	return "NORMAL"
}

// visibility hides the words on a locked handset set to conceal sensitive
// content, showing only that something arrived.
//
// PRIVATE is the platform's own default for a channel; sending it explicitly
// means the app's channel settings cannot quietly widen it.
func visibility(private bool) string {
	if private {
		return "PRIVATE"
	}
	return ""
}

// apns builds the iOS half.
//
// Split out because its rules are not Android's and do not read as variations
// of them: a silent push is a different *push type* to APNs, not the same push
// with the words left out, and sending it at the wrong priority has it refused
// outright rather than delivered quietly.
func (c *Client) apns(m push.Message) *apnsConfig {
	cfg := &apnsConfig{Headers: map[string]string{}}

	if m.Silent {
		cfg.Headers["apns-push-type"] = "background"
		// Must be 5. APNs rejects a background push sent at 10, and the
		// rejection is an INVALID_ARGUMENT that looks like a payload bug.
		cfg.Headers["apns-priority"] = "5"
		cfg.Payload = &apnsPayload{APS: apsDictionary{ContentAvailable: 1}}
	} else {
		cfg.Headers["apns-push-type"] = "alert"
		cfg.Headers["apns-priority"] = "10"
		if m.Priority != push.PriorityHigh {
			cfg.Headers["apns-priority"] = "5"
		}
		aps := apsDictionary{Sound: "default"}
		if m.Image != "" {
			aps.MutableContent = 1
		}
		cfg.Payload = &apnsPayload{APS: aps}

		if m.ReplaceID != "" {
			cfg.Headers["apns-collapse-id"] = fitCollapseID(m.ReplaceID)
		}
	}

	if m.Lifetime > 0 {
		// An absolute unix second, not a duration — the one place the two
		// platforms disagree on how to say the same thing.
		cfg.Headers["apns-expiration"] = strconv.FormatInt(c.cfg.Now().Add(m.Lifetime).Unix(), 10)
	}
	return cfg
}

// fitCollapseID fits a replace id into this platform's 64-byte header limit.
//
// Hashed rather than truncated. An over-long id fails the whole send, and
// truncating would make two ids sharing a 64-byte prefix replace each other. A
// SHA-256 in hex is exactly 64 bytes, and the same id always hashes the same —
// which is the only property the id has to have.
func fitCollapseID(id string) string {
	const limit = 64
	if len(id) <= limit {
		return id
	}
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])
}

// accessToken returns a cached token, minting one when it is missing or nearly
// expired.
//
// The lock is held across the network call, which serialises every send behind
// one refresh. That is deliberate: the alternative lets a hundred queue workers
// each mint their own token on a cold start, and the vendor rate-limits the
// token endpoint far more tightly than the send endpoint.
func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.cfg.Now()
	if c.token != "" && now.Before(c.expires.Add(-refreshBefore)) {
		return c.token, nil
	}

	token, expiresIn, err := c.mintToken(ctx)
	if err != nil {
		return "", err
	}
	c.token, c.expires = token, now.Add(expiresIn)
	return token, nil
}

func (c *Client) mintToken(ctx context.Context) (string, time.Duration, error) {
	endpoint := c.cfg.TokenHost + "/token"

	assertion, err := c.assertion(endpoint)
	if err != nil {
		return "", 0, err
	}

	form := url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  {assertion},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	status, body, err := c.do(req)
	if err != nil {
		return "", 0, err
	}
	if status < 200 || status >= 300 {
		// Reported without the body's detail beyond a cap, for the same reason
		// as elsewhere: a vendor error body is not a safe thing to log whole.
		return "", 0, fmt.Errorf("token endpoint: %s", vendorReason(status, body))
	}

	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		return "", 0, fmt.Errorf("token endpoint returned unreadable JSON: %w", err)
	}
	if out.AccessToken == "" {
		return "", 0, errors.New("token endpoint returned no access_token")
	}
	if out.ExpiresIn <= 0 {
		out.ExpiresIn = 3600
	}
	return out.AccessToken, time.Duration(out.ExpiresIn) * time.Second, nil
}

// assertion builds and signs the JWT the token endpoint trades for an access
// token.
func (c *Client) assertion(audience string) (string, error) {
	now := c.cfg.Now()

	header, err := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(map[string]any{
		"iss":   c.cfg.ClientEmail,
		"scope": messagingScope,
		"aud":   audience,
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	})
	if err != nil {
		return "", err
	}

	signing := encodeSegment(header) + "." + encodeSegment(claims)
	digest := sha256Sum([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, c.key, crypto.SHA256, digest)
	if err != nil {
		return "", fmt.Errorf("fcm: signing the assertion: %w", err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func encodeSegment(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// parseKey accepts the PKCS#8 block a service-account key file carries, and
// PKCS#1 besides, because a key rotated by hand through openssl often comes out
// as the latter and the failure is otherwise a flat "failed to parse".
func parseKey(pemStr string) (*rsa.PrivateKey, error) {
	// A literal backslash-n becomes a newline first. The key arrives inside a
	// JSON credential file, and whoever moved it into an environment variable
	// very often moved the escaping with it. Refusing that is a puzzle rather
	// than a guard.
	pemStr = strings.ReplaceAll(pemStr, `\n`, "\n")

	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errors.New("fcm: PrivateKeyPEM is not PEM — expected a -----BEGIN PRIVATE KEY----- block")
	}

	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		rsaKey, ok := key.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("fcm: key is %T, want RSA", key)
		}
		return rsaKey, nil
	}
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("fcm: unreadable private key: %w", err)
	}
	return key, nil
}
