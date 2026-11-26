package fcm

import (
	"errors"
	"fmt"
	"time"

	"github.com/zamryeng/zamry_push_golang/push"
)

// CheckCatalogue reports every declaration this vendor would refuse at send
// time.
//
// Call it at boot, beside [push.NewCatalogue]. That constructor refuses what
// the package itself can judge and deliberately knows nothing about FCM, so a
// limit that belongs to the vendor has to be checked by the vendor — and
// checked here rather than on the first send, because the alternative is a
// kind that boots clean, renders clean, and is then refused permanently on
// every send and dropped. One person is not told something, and the only
// trace is a log line nobody is reading.
//
// Every problem is reported, not just the first: a declaration file is edited
// once and fixed once, so a boot that names one of four mistakes costs four
// restarts.
func CheckCatalogue(c *push.Catalogue) error {
	var problems []error
	for _, k := range c.Kinds() {
		if k.Lifetime > MaxLifetime {
			problems = append(problems, fmt.Errorf(
				"kind %q declares a lifetime of %s, above this vendor's maximum of %s",
				k.Name, k.Lifetime, MaxLifetime))
		}
		// Under a second formats as a ttl of "0s", which the vendor reads as
		// deliver-now-or-drop. Legal, and never what the declaration looks
		// like it means.
		if k.Lifetime > 0 && k.Lifetime < time.Second {
			problems = append(problems, fmt.Errorf(
				"kind %q declares a lifetime of %s, which this vendor rounds to deliver-or-drop",
				k.Name, k.Lifetime))
		}
	}
	return errors.Join(problems...)
}
