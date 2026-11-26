package fcm

import (
	"testing"

	"github.com/zamryeng/zamry_push_golang/push"
	"github.com/zamryeng/zamry_push_golang/pushtest"
)

// The same suite the console runs, against the real client over an httptest
// vendor. Running it on only one of the two would prove nothing about the pair.
func TestClientBroadcasterConformance(t *testing.T) {
	pushtest.Broadcaster(t, aTopic(t), func(t *testing.T) push.Broadcaster {
		return testClient(t, newVendor(t))
	})
}

func TestClientTopicMembershipConformance(t *testing.T) {
	pushtest.TopicMembership(t, aTopic(t), func(t *testing.T) push.TopicMembership {
		return testClient(t, newVendor(t))
	})
}

func TestClientVendorConformance(t *testing.T) {
	pushtest.Vendor(t, func(t *testing.T) push.Vendor {
		return testClient(t, newVendor(t))
	})
}
