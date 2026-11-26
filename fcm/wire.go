package fcm

import "crypto/sha256"

// The vendor's HTTP v1 message shape.
//
// Written out rather than built from a map so a field name the vendor rejects
// is a compile error in one place, and so the omitempty rules are visible: the
// vendor refuses an empty notification object, an empty apns payload and a
// zero-length ttl, each with an INVALID_ARGUMENT that names something else.

type sendRequest struct {
	ValidateOnly bool          `json:"validate_only,omitempty"`
	Message      vendorMessage `json:"message"`
}

type vendorMessage struct {
	Token        string              `json:"token,omitempty"`
	Topic        string              `json:"topic,omitempty"`
	Data         map[string]string   `json:"data,omitempty"`
	Notification *vendorNotification `json:"notification,omitempty"`
	Android      *androidConfig      `json:"android,omitempty"`
	APNS         *apnsConfig         `json:"apns,omitempty"`
}

type vendorNotification struct {
	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty"`
	Image string `json:"image,omitempty"`
}

type androidConfig struct {
	Priority     string               `json:"priority,omitempty"`
	TTL          string               `json:"ttl,omitempty"`
	Notification *androidNotification `json:"notification,omitempty"`
}

type androidNotification struct {
	ChannelID string `json:"channel_id,omitempty"`

	// Tag replaces a notification already showing with the same tag, which is
	// what makes a duplicate send show once.
	//
	// The tag rather than android.collapse_key: a collapse key only merges
	// messages the vendor is still holding for an offline handset, and it
	// keeps at most four per device — so a key per message would make every
	// message collapsible and a handset offline through five events could
	// lose some.
	Tag string `json:"tag,omitempty"`

	// Visibility is how a locked screen shows it. Empty leaves Android's
	// own default for the channel.
	Visibility string `json:"visibility,omitempty"`
}

type apnsConfig struct {
	Headers map[string]string `json:"headers,omitempty"`
	Payload *apnsPayload      `json:"payload,omitempty"`
}

type apnsPayload struct {
	APS apsDictionary `json:"aps"`
}

type apsDictionary struct {
	// ContentAvailable is 1 on a silent push and absent otherwise. Sending 0
	// explicitly is not the same as omitting it: iOS treats the key's presence
	// as the request, so a zero would ask for a background wake on every
	// ordinary notification.
	ContentAvailable int    `json:"content-available,omitempty"`
	Sound            string `json:"sound,omitempty"`

	// MutableContent lets a client-side extension fetch and render an image.
	// Without it the image simply does not appear on that platform, silently.
	MutableContent int `json:"mutable-content,omitempty"`
}

type membershipRequest struct {
	To     string   `json:"to"`
	Tokens []string `json:"registration_tokens"`
}

// membershipResponse answers positionally: one result per token sent, an empty
// object for each one that succeeded.
type membershipResponse struct {
	Results []struct {
		Error string `json:"error"`
	} `json:"results"`
}

func sha256Sum(b []byte) []byte {
	sum := sha256.Sum256(b)
	return sum[:]
}
