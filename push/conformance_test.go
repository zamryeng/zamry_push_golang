package push_test

import (
	"testing"
	"time"

	"github.com/zamryeng/zamry_push_golang/push"
	"github.com/zamryeng/zamry_push_golang/pushtest"
)

func TestMemoryStoreConformance(t *testing.T) {
	pushtest.Store(t, func(*testing.T) push.Store { return push.NewMemoryStore() })
}

func TestMemoryQueueConformance(t *testing.T) {
	pushtest.JobStore(t, func(_ *testing.T, lease time.Duration) push.JobStore {
		q := push.NewMemoryQueue()
		q.Lease = lease
		return q
	})
}

// The last storage port that had two implementations and no shared suite.
func TestMemoryCopyStoreConformance(t *testing.T) {
	pushtest.CopyStore(t, func(*testing.T) push.CopyStore { return push.NewMemoryStore() })
}

// Both shipped recipient models run the same suite: what they share is small,
// and every part of it fails silently.
func TestEveryTokenOfAccountConformance(t *testing.T) {
	pushtest.Recipients(t, func(_ *testing.T, store push.Store) push.Recipients {
		return push.EveryTokenOfAccount(store)
	})
}

func TestDeviceOfAccountConformance(t *testing.T) {
	pushtest.Recipients(t, func(_ *testing.T, store push.Store) push.Recipients {
		return push.DeviceOfAccount(store)
	})
}
