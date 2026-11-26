package fcm

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/zamryeng/zamry_push_golang/push"
)

func aTopic(t *testing.T) push.Topic {
	t.Helper()
	topic, err := push.MustTopicSet("marketing").Parse("marketing")
	if err != nil {
		t.Fatal(err)
	}
	return topic
}

// ---------- replace id (gaps 5, 6) ----------

func TestReplaceIDReachesBothPlatforms(t *testing.T) {
	v := newVendor(t)
	c := testClient(t, v)

	m := aMessage()
	m.ReplaceID = "transfer-42"
	c.Send(context.Background(), "tok-1", m)

	got := v.lastSend.Message
	if got.Android.Notification.Tag != "transfer-42" {
		t.Errorf("android tag = %q — the tag, not collapse_key: a collapse key only "+
			"merges messages the vendor still holds, and it keeps four per device",
			got.Android.Notification.Tag)
	}
	if h := got.APNS.Headers["apns-collapse-id"]; h != "transfer-42" {
		t.Errorf("apns-collapse-id = %q", h)
	}
}

// An over-long id fails the whole send, and truncating would make two ids
// sharing a 64-byte prefix replace each other.
func TestAnOverlongReplaceIDIsHashedNotTruncated(t *testing.T) {
	v := newVendor(t)
	c := testClient(t, v)

	long := strings.Repeat("a", 100) + "-one"
	other := strings.Repeat("a", 100) + "-two"

	m := aMessage()
	m.ReplaceID = long
	c.Send(context.Background(), "tok-1", m)
	first := v.lastSend.Message.APNS.Headers["apns-collapse-id"]

	if len(first) != 64 {
		t.Fatalf("collapse id is %d bytes, want exactly the 64-byte limit", len(first))
	}
	sum := sha256.Sum256([]byte(long))
	if first != hex.EncodeToString(sum[:]) {
		t.Errorf("collapse id = %q, want the hash of the id", first)
	}

	m.ReplaceID = other
	c.Send(context.Background(), "tok-1", m)
	second := v.lastSend.Message.APNS.Headers["apns-collapse-id"]

	if first == second {
		t.Fatal("two ids sharing a 64-byte prefix collapsed onto each other, so one " +
			"notification would replace an unrelated one")
	}

	// The same id must always produce the same value, which is the only
	// property it has to have.
	m.ReplaceID = long
	c.Send(context.Background(), "tok-1", m)
	if again := v.lastSend.Message.APNS.Headers["apns-collapse-id"]; again != first {
		t.Error("the same id hashed differently, so two sends of one event would not replace")
	}
}

func TestNoReplaceIDMeansNoCollapseHeader(t *testing.T) {
	v := newVendor(t)
	c := testClient(t, v)

	c.Send(context.Background(), "tok-1", aMessage())
	if _, present := v.lastSend.Message.APNS.Headers["apns-collapse-id"]; present {
		t.Error("a message with no replace id carried a collapse header")
	}
	if tag := v.lastSend.Message.Android.Notification.Tag; tag != "" {
		t.Errorf("android tag = %q", tag)
	}
}

// ---------- lock screen (gap 7) ----------

func TestPrivateMessagesHideTheirWordsOnALockedScreen(t *testing.T) {
	v := newVendor(t)
	c := testClient(t, v)

	m := aMessage()
	m.Private = true
	c.Send(context.Background(), "tok-1", m)
	if got := v.lastSend.Message.Android.Notification.Visibility; got != "PRIVATE" {
		t.Errorf("visibility = %q — a locked screen set to hide sensitive content would "+
			"otherwise show the amount", got)
	}

	m.Private = false
	c.Send(context.Background(), "tok-1", m)
	if got := v.lastSend.Message.Android.Notification.Visibility; got != "" {
		t.Errorf("visibility = %q, want the platform's own default left alone", got)
	}
}

// ---------- image (gap 11) ----------

func TestAnImageCarriesTheFlagThatRendersIt(t *testing.T) {
	v := newVendor(t)
	c := testClient(t, v)

	m := aMessage()
	m.Image = "https://example.test/receipt.png"
	c.Send(context.Background(), "tok-1", m)

	got := v.lastSend.Message
	if got.Notification.Image != m.Image {
		t.Errorf("image = %q", got.Notification.Image)
	}
	if got.APNS.Payload.APS.MutableContent != 1 {
		t.Error("without mutable-content the image simply does not appear on that " +
			"platform, silently")
	}

	m.Image = ""
	c.Send(context.Background(), "tok-1", m)
	if v.lastSend.Message.APNS.Payload.APS.MutableContent != 0 {
		t.Error("mutable-content was set with no image to render")
	}
}

// ---------- silent (gaps 13, 14) ----------

func TestASilentSendIsAlwaysHighPriorityOnAndroid(t *testing.T) {
	v := newVendor(t)
	c := testClient(t, v)

	silent := aMessage()
	silent.Silent, silent.Title, silent.Body = true, "", ""
	silent.Priority = push.PriorityNormal

	c.Send(context.Background(), "tok-1", silent)

	if got := v.lastSend.Message.Android.Priority; got != "HIGH" {
		t.Fatalf("android priority = %q — below HIGH the platform holds a data message "+
			"until the handset leaves Doze, and a signal delivered hours late is one "+
			"the app had already acted on", got)
	}
	if v.lastSend.Message.Android.Notification != nil {
		t.Error("a silent send carried a notification block, filing it for a display " +
			"that is not going to happen")
	}
}

func TestBroadcastSignalRefusesEmptyData(t *testing.T) {
	v := newVendor(t)
	c := testClient(t, v)

	out := c.BroadcastSignal(context.Background(), aTopic(t), push.Signal{Lifetime: time.Minute})
	if out.Kind != push.Permanent {
		t.Fatalf("outcome = %+v, want Permanent — a handset woken for an empty payload "+
			"has burned a wakeup for nothing", out)
	}
	if v.sends.Load() != 0 {
		t.Error("it reached the vendor anyway")
	}
}

// ---------- broadcast (gaps 2, 4) ----------

func TestBroadcastAddressesATopicAndCarriesOnlyTheLink(t *testing.T) {
	v := newVendor(t)
	c := testClient(t, v)

	out := c.Broadcast(context.Background(), aTopic(t), push.Announcement{
		Title: "This weekend", Body: "Have a look.", Link: "/offers",
		ChannelID: "marketing", Priority: push.PriorityNormal, Lifetime: 24 * time.Hour,
	})
	if out.Kind != push.Delivered {
		t.Fatalf("outcome = %+v", out)
	}

	m := v.lastSend.Message
	if m.Topic != "marketing" {
		t.Errorf("topic = %q", m.Topic)
	}
	if m.Token != "" {
		t.Errorf("token = %q — a topic send addresses no device", m.Token)
	}
	if len(m.Data) != 1 || m.Data["link"] != "/offers" {
		t.Errorf("data = %v — the link is the only thing an announcement's data may "+
			"carry, because data is where an account id would smuggle itself onto a "+
			"public channel", m.Data)
	}
	if out.Token != "" {
		t.Errorf("outcome token = %q — there is no row a topic failure belongs to", out.Token)
	}
}

func TestBroadcastRefusesAnUndeclaredTopic(t *testing.T) {
	v := newVendor(t)
	c := testClient(t, v)

	var zero push.Topic
	out := c.Broadcast(context.Background(), zero, push.Announcement{Title: "t", Body: "b"})
	if out.Kind != push.Permanent {
		t.Fatalf("outcome = %+v, want Permanent", out)
	}
	if v.sends.Load() != 0 {
		t.Error("a zero topic reached the vendor")
	}
}

func TestBroadcastRefusesAnAnnouncementWithNoWords(t *testing.T) {
	v := newVendor(t)
	c := testClient(t, v)

	if out := c.Broadcast(context.Background(), aTopic(t),
		push.Announcement{Body: "b"}); out.Kind != push.Permanent {
		t.Errorf("no title = %+v", out)
	}
	if out := c.Broadcast(context.Background(), aTopic(t),
		push.Announcement{Title: "t"}); out.Kind != push.Permanent {
		t.Errorf("no body = %+v", out)
	}
	if v.sends.Load() != 0 {
		t.Error("it reached the vendor")
	}
}

func TestBroadcastSignalIsDataOnly(t *testing.T) {
	v := newVendor(t)
	c := testClient(t, v)

	out := c.BroadcastSignal(context.Background(), aTopic(t), push.Signal{
		Data: map[string]string{"type": "catalogue_changed"}, Lifetime: 10 * time.Minute,
	})
	if out.Kind != push.Delivered {
		t.Fatalf("outcome = %+v", out)
	}

	m := v.lastSend.Message
	if m.Notification != nil {
		t.Error("a silent broadcast carried words")
	}
	if m.Topic != "marketing" {
		t.Errorf("topic = %q", m.Topic)
	}
	if m.APNS.Headers["apns-push-type"] != "background" {
		t.Errorf("apns-push-type = %q", m.APNS.Headers["apns-push-type"])
	}
	if m.APNS.Payload.APS.ContentAvailable != 1 {
		t.Error("content-available must be 1 or iOS never wakes the app")
	}
}

// A topic send has no token, so a stale-token answer cannot be about one.
// Retiring something on the strength of it would retire whatever row happened
// to be nearby.
func TestAStaleTokenAnswerToATopicSendIsNotAStaleToken(t *testing.T) {
	v := newVendor(t)
	c := testClient(t, v)
	v.status, v.body = 404, bodyUnregistered

	out := c.Broadcast(context.Background(), aTopic(t),
		push.Announcement{Title: "t", Body: "b", ChannelID: "marketing"})

	if out.Kind == push.StaleToken {
		t.Fatal("a topic send reported a stale token, and there is no token for it to mean")
	}
	if out.Kind != push.Permanent {
		t.Errorf("outcome = %+v, want Permanent", out)
	}
}

// ---------- membership (gap 3) ----------

func TestSubscribeAndUnsubscribe(t *testing.T) {
	v := newVendor(t)
	c := testClient(t, v)
	ctx := context.Background()

	refused, err := c.Subscribe(ctx, aTopic(t), []string{"tok-a", "tok-b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(refused) != 0 {
		t.Errorf("refused = %v", refused)
	}
	if v.lastMembership.To != "/topics/marketing" {
		t.Errorf("to = %q", v.lastMembership.To)
	}
	if len(v.lastMembership.Tokens) != 2 {
		t.Errorf("tokens = %v", v.lastMembership.Tokens)
	}
	if v.lastMembershipOp != "batchAdd" {
		t.Errorf("op = %q", v.lastMembershipOp)
	}
	if v.lastMembershipHeader != "true" {
		t.Error("access_token_auth is required by this endpoint, and omitting it is a " +
			"400 that says nothing about the header")
	}

	if _, err := c.Unsubscribe(ctx, aTopic(t), []string{"tok-a"}); err != nil {
		t.Fatal(err)
	}
	if v.lastMembershipOp != "batchRemove" {
		t.Errorf("op = %q", v.lastMembershipOp)
	}
}

// The vendor answers per token, so a call that succeeded as a whole still
// reports the ones it refused.
func TestMembershipAnswersTheTokensRefused(t *testing.T) {
	v := newVendor(t)
	c := testClient(t, v)
	v.membershipResults = []string{"", "NOT_FOUND", ""}

	refused, err := c.Subscribe(context.Background(), aTopic(t),
		[]string{"tok-a", "tok-dead", "tok-c"})
	if err != nil {
		t.Fatal(err)
	}
	if len(refused) != 1 || refused[0] != "tok-dead" {
		t.Fatalf("refused = %v — the answer is positional, so the wrong index names "+
			"the wrong token", refused)
	}
}

// A caller setting a fresh deployment's topics hands over more than the vendor
// takes in one call, and nobody should discover that limit from a refusal.
func TestMembershipSplitsALongList(t *testing.T) {
	v := newVendor(t)
	c := testClient(t, v)

	tokens := make([]string, 2500)
	for i := range tokens {
		tokens[i] = "tok-" + strings.Repeat("x", 3) + string(rune('a'+i%26)) + hex.EncodeToString([]byte{byte(i), byte(i >> 8)})
	}

	if _, err := c.Subscribe(context.Background(), aTopic(t), tokens); err != nil {
		t.Fatal(err)
	}
	if got := v.membershipCalls.Load(); got != 3 {
		t.Fatalf("made %d calls for 2500 tokens, want 3 batches of at most %d", got, maxTopicBatch)
	}
	if got := len(v.lastMembership.Tokens); got != 500 {
		t.Errorf("the last batch had %d tokens, want the remainder", got)
	}
}

func TestMembershipRefusals(t *testing.T) {
	v := newVendor(t)
	c := testClient(t, v)
	ctx := context.Background()

	var zero push.Topic
	if _, err := c.Subscribe(ctx, zero, []string{"tok-a"}); err == nil {
		t.Error("an undeclared topic was accepted")
	}
	if _, err := c.Subscribe(ctx, aTopic(t), []string{"tok-a", ""}); err == nil {
		t.Error("an empty token was accepted")
	}
	if v.membershipCalls.Load() != 0 {
		t.Error("a refused membership change reached the vendor")
	}

	// An empty list is a no-op rather than a call.
	if refused, err := c.Subscribe(ctx, aTopic(t), nil); err != nil || refused != nil {
		t.Errorf("empty list = %v, %v", refused, err)
	}
	if v.membershipCalls.Load() != 0 {
		t.Error("an empty list made a call")
	}
}

func TestAMembershipOutageIsAnError(t *testing.T) {
	v := newVendor(t)
	c := testClient(t, v)
	v.membershipStatus = http.StatusServiceUnavailable

	if _, err := c.Subscribe(context.Background(), aTopic(t), []string{"tok-a"}); err == nil {
		t.Fatal("an outage was reported as success, so a caller would believe the " +
			"token is subscribed when it is not")
	}
}

// ---------- credentials (gaps 8, 9, 10) ----------

func TestAnUnauthorizedSendForgetsTheAccessToken(t *testing.T) {
	v := newVendor(t)
	c := testClient(t, v)

	c.Send(context.Background(), "tok-1", aMessage())
	if v.mints.Load() != 1 {
		t.Fatalf("mints = %d", v.mints.Load())
	}

	v.status, v.body = http.StatusUnauthorized, bodyUnauthenticated
	c.Send(context.Background(), "tok-1", aMessage())

	v.status, v.body = 200, `{"name":"ok"}`
	c.Send(context.Background(), "tok-1", aMessage())

	// Two, not three: the refused send spent the cached token and dropped it,
	// and only the send after it had to mint. Without the drop this is one,
	// because the cached token would have been reused for an hour.
	if got := v.mints.Load(); got != 2 {
		t.Fatalf("minted %d tokens — a 401 must drop the cached one, or a rotated "+
			"credential fails every send for up to an hour after it was fixed", got)
	}
}

func TestAnUnauthorizedMembershipCallForgetsTheAccessToken(t *testing.T) {
	v := newVendor(t)
	c := testClient(t, v)

	if _, err := c.Subscribe(context.Background(), aTopic(t), []string{"tok-a"}); err != nil {
		t.Fatal(err)
	}
	v.membershipStatus = http.StatusUnauthorized
	_, _ = c.Subscribe(context.Background(), aTopic(t), []string{"tok-a"})

	v.membershipStatus = 200
	if _, err := c.Subscribe(context.Background(), aTopic(t), []string{"tok-a"}); err != nil {
		t.Fatal(err)
	}
	// Two for the same reason as a refused send: the 401 spent the cached
	// token, the call after it minted. One would mean the 401 was ignored.
	if got := v.mints.Load(); got != 2 {
		t.Fatalf("minted %d tokens, want the 401 to have dropped the cached one", got)
	}
}

func TestWarmObtainsTheTokenTheFirstSendWouldHavePaidFor(t *testing.T) {
	v := newVendor(t)
	c := testClient(t, v)

	if err := c.Warm(context.Background()); err != nil {
		t.Fatal(err)
	}
	if v.mints.Load() != 1 {
		t.Fatalf("mints = %d", v.mints.Load())
	}

	c.Send(context.Background(), "tok-1", aMessage())
	if got := v.mints.Load(); got != 1 {
		t.Errorf("the first send minted again (%d), so Warm bought nothing", got)
	}
}

// The key arrives inside a JSON credential file, and whoever moved it into an
// environment variable very often moved the escaping with it.
func TestAKeyWithEscapedNewlinesIsAccepted(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	real := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	escaped := strings.ReplaceAll(real, "\n", `\n`)

	if _, err := New(Config{
		ProjectID: "p", ClientEmail: "a@b", PrivateKeyPEM: escaped,
	}); err != nil {
		t.Fatalf("a key with escaped newlines was refused, which is a puzzle rather "+
			"than a guard: %v", err)
	}
}

func TestParseServiceAccountHandlesAnEscapedKey(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	real := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))

	file, err := json.Marshal(map[string]string{
		"type": "service_account", "project_id": "p",
		"client_email": "a@b", "private_key": real,
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ParseServiceAccount(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(cfg); err != nil {
		t.Fatal(err)
	}
}

// ---------- the guards on a direct call (regression) ----------

// Refused rather than defaulted, for the same reason Priority counts from one.
// These were accepted once: an announcement with no priority went out as NORMAL
// and one with no lifetime went out with no ttl at all.
func TestADirectBroadcastRefusesAnIncompleteAnnouncement(t *testing.T) {
	cases := map[string]push.Announcement{
		"no priority": {Title: "t", Body: "b", ChannelID: "marketing", Lifetime: time.Hour},
		"no lifetime": {Title: "t", Body: "b", ChannelID: "marketing", Priority: push.PriorityNormal},
	}
	for name, a := range cases {
		t.Run(name, func(t *testing.T) {
			v := newVendor(t)
			c := testClient(t, v)

			if out := c.Broadcast(context.Background(), aTopic(t), a); out.Kind != push.Permanent {
				t.Fatalf("outcome = %+v, want Permanent", out)
			}
			if v.sends.Load() != 0 {
				t.Error("it reached the vendor")
			}
		})
	}
}

func TestADirectSignalRefusesNoLifetime(t *testing.T) {
	v := newVendor(t)
	c := testClient(t, v)

	out := c.BroadcastSignal(context.Background(), aTopic(t), push.Signal{
		Data: map[string]string{"type": "x"},
	})
	if out.Kind != push.Permanent {
		t.Fatalf("outcome = %+v, want Permanent", out)
	}
	if v.sends.Load() != 0 {
		t.Error("it reached the vendor")
	}
}

func TestADirectSendRefusesAnIncompleteMessage(t *testing.T) {
	incomplete := func(mutate func(*push.Message)) push.Message {
		m := aMessage()
		mutate(&m)
		return m
	}
	cases := map[string]push.Message{
		"no title":    incomplete(func(m *push.Message) { m.Title = "" }),
		"no body":     incomplete(func(m *push.Message) { m.Body = "" }),
		"no priority": incomplete(func(m *push.Message) { m.Priority = 0 }),
		"no lifetime": incomplete(func(m *push.Message) { m.Lifetime = 0 }),
		"silent with no data": incomplete(func(m *push.Message) {
			m.Silent, m.Title, m.Body, m.Data = true, "", "", nil
		}),
	}
	for name, m := range cases {
		t.Run(name, func(t *testing.T) {
			v := newVendor(t)
			c := testClient(t, v)

			out := c.Send(context.Background(), "tok-1", m)
			if out.Kind != push.Permanent {
				t.Fatalf("outcome = %+v, want Permanent", out)
			}
			if out.Token != "tok-1" {
				t.Errorf("token = %q", out.Token)
			}
			if v.sends.Load() != 0 {
				t.Error("it reached the vendor")
			}
		})
	}
}

// The per-device and topic paths share one POST, so a rule that belongs to one
// must not reach the other. A stale token on a device send is exactly what it
// says; rewriting it would mean no dead row is ever retired.
func TestTheSharedPostKeepsTheTwoPathsDistinct(t *testing.T) {
	v := newVendor(t)
	c := testClient(t, v)
	v.status, v.body = 404, bodyUnregistered

	device := c.Send(context.Background(), "tok-dead", aMessage())
	if device.Kind != push.StaleToken {
		t.Fatalf("device send = %v, want StaleToken", device.Kind)
	}
	if device.Token != "tok-dead" {
		t.Errorf("token = %q — the caller cannot retire a row it is not told about", device.Token)
	}

	topic := c.Broadcast(context.Background(), aTopic(t), push.Announcement{
		Title: "t", Body: "b", ChannelID: "marketing",
		Priority: push.PriorityNormal, Lifetime: time.Hour,
	})
	if topic.Kind != push.Permanent {
		t.Fatalf("topic send = %v, want Permanent", topic.Kind)
	}
	if topic.Token != "" {
		t.Errorf("topic outcome carried token %q, and there is no row it belongs to", topic.Token)
	}
}

// A positional answer of the wrong length cannot be trusted either way.
func TestMembershipRefusesAMismatchedAnswer(t *testing.T) {
	v := newVendor(t)
	c := testClient(t, v)
	v.membershipResults = []string{""} // one result for two tokens

	refused, err := c.Subscribe(context.Background(), aTopic(t), []string{"tok-a", "tok-b"})
	if err == nil {
		t.Fatal("a short answer was accepted: calling a good token refused retires it, " +
			"and calling a refused one accepted leaves a handset silently off the topic")
	}
	if refused != nil {
		t.Errorf("refused = %v, want nothing claimed either way", refused)
	}
}

// A silent send with words is a contradiction the envelope resolves by
// dropping them, which would show as a delivered outcome and no notification.
func TestASilentMessageCannotCarryWords(t *testing.T) {
	v := newVendor(t)
	c := testClient(t, v)

	m := aMessage()
	m.Silent = true // but Title and Body are still set

	out := c.Send(context.Background(), "tok-1", m)
	if out.Kind != push.Permanent {
		t.Fatalf("outcome = %+v, want Permanent — otherwise the caller sees a delivery "+
			"and nobody sees a notification", out)
	}
	if v.sends.Load() != 0 {
		t.Error("it reached the vendor")
	}
}
