package integration

import (
	"testing"
	"time"

	"github.com/zamryeng/zamry_push_golang/push"
	"github.com/zamryeng/zamry_push_golang/pushtest"
)

// The same suite the memory implementations run. That is the whole point: a
// consumer's tests exercise the memory ones, and these prove the two agree
// about everything that suite asserts.
func TestPostgresStoreConformance(t *testing.T) {
	pushtest.Store(t, func(t *testing.T) push.Store {
		s, _ := testStore(t)
		return s
	})
}

func TestPostgresJobStoreConformance(t *testing.T) {
	pushtest.JobStore(t, func(t *testing.T, lease time.Duration) push.JobStore {
		s, _ := testStore(t)
		s.Lease = lease
		return s
	})
}

// The copy table decides what words reach a handset and keeps an audit row
// saying who changed them. It was the last storage port with two
// implementations and no shared suite.
func TestPostgresCopyStoreConformance(t *testing.T) {
	pushtest.CopyStore(t, func(t *testing.T) push.CopyStore {
		s, _ := testStore(t)
		return s
	})
}
