package pushhttp

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zamryeng/zamry_push_golang/push"
)

// countingBody reports how much of itself was actually read.
type countingBody struct {
	r    io.Reader
	read int64
}

func (c *countingBody) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.read += int64(n)
	return n, err
}

func (c *countingBody) Close() error { return nil }

// The cap has to bound what is READ, not only what is decoded.
//
// This is a regression test for a real one: the handler drained the request
// body after a refusal so the connection stayed reusable, and drained the raw
// body rather than the capped reader. The decoder stopped at the limit and the
// drain then read the remaining four megabytes — the tidying defeated the cap
// it sat next to.
func TestBodyCapBoundsWhatIsRead(t *testing.T) {
	const cap = 1024
	api, err := New(Config{
		Store: push.NewMemoryStore(), Catalogue: testCatalogue(t),
		Identify: identifyAs("acct-1", nil), MaxBodyBytes: cap,
	})
	if err != nil {
		t.Fatal(err)
	}

	const huge = 4 << 20
	body := &countingBody{r: strings.NewReader(strings.Repeat("x", huge))}

	r := httptest.NewRequest("POST", "/notifications/token", body)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	api.Handler().ServeHTTP(w, r)

	if body.read > cap*4 {
		t.Fatalf("read %d bytes of a %d-byte body against a %d-byte cap", body.read, huge, cap)
	}
}
