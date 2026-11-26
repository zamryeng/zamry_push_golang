package push

import (
	"fmt"
	"strings"
)

// Unicode directional isolates.
//
// Every substituted value is wrapped in these. An address, an amount or a name
// is often a left-to-right run inside a right-to-left sentence, and without an
// isolate the bidirectional algorithm reorders the characters around it: an
// address renders with its digits transposed. It looks like a font problem and
// is a correctness one — the reader sees a different address from the one sent.
const (
	isolateStart = "⁨" // FSI: first strong isolate
	isolateEnd   = "⁩" // PDI: pop directional isolate
)

// Placeholders are the field names a template substitutes, in order of first
// appearance.
//
// A template may name the same field twice; it appears once here.
func Placeholders(template string) []string {
	var out []string
	seen := map[string]bool{}
	rest := template
	for {
		open := strings.IndexByte(rest, '{')
		if open < 0 {
			return out
		}
		rest = rest[open+1:]
		shut := strings.IndexByte(rest, '}')
		if shut < 0 {
			return out
		}
		name := rest[:shut]
		rest = rest[shut+1:]
		if plausibleName(name) && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
}

// ValidateCopy reports whether this wording only names fields the kind
// declares.
//
// Checked when copy is written, not only at boot: wording changes without a
// deploy, so boot-time validation would pass and the handset would still
// receive a literal brace. The write is the last moment anyone is watching.
func ValidateCopy(k Kind, c Copy) error {
	declared := make(map[string]bool, len(k.Fields))
	for _, f := range k.Fields {
		declared[f] = true
	}
	for _, text := range []string{c.Title, c.Body} {
		// Every brace pair, not only the ones that look like field names.
		// [Placeholders] drops implausible ones, and reusing it here left the
		// exact shape of an operator's typo unchecked: {Amount}, {amount-usd}
		// and { amount } all passed this and then rendered as a literal brace
		// on the handset, which is the one thing this function exists to
		// prevent.
		for _, p := range braced(text) {
			if !plausibleName(p) {
				return fmt.Errorf("%w: %q is not a usable placeholder in copy for kind %q (%s)",
					ErrMalformed, "{"+p+"}", k.Name, c.Language)
			}
			if !declared[p] {
				return fmt.Errorf("%w: %q in copy for kind %q (%s)",
					ErrUnknownPlaceholder, p, k.Name, c.Language)
			}
		}
	}
	return nil
}

// braced is every name between a brace pair, in order, whether or not it is a
// name this package would accept.
//
// Separate from [Placeholders], which answers "which fields does this wording
// read" for callers listing them. This one answers "what did somebody type",
// which is the question validation has to ask.
func braced(template string) []string {
	var out []string
	rest := template
	for {
		open := strings.IndexByte(rest, '{')
		if open < 0 {
			return out
		}
		rest = rest[open+1:]
		shut := strings.IndexByte(rest, '}')
		if shut < 0 {
			return out
		}
		out = append(out, rest[:shut])
		rest = rest[shut+1:]
	}
}

// Render substitutes a kind's fields into one piece of wording.
//
// A placeholder with no value is left as it stands rather than emptied. A
// sentence with a hole in it reads as a bug and gets reported; a sentence
// quietly missing its amount reads as a different, wrong sentence.
func Render(template string, f Fields) string {
	if len(f) == 0 {
		return template
	}
	var b strings.Builder
	b.Grow(len(template))
	rest := template
	for {
		open := strings.IndexByte(rest, '{')
		if open < 0 {
			b.WriteString(rest)
			return b.String()
		}
		shut := strings.IndexByte(rest[open:], '}')
		if shut < 0 {
			b.WriteString(rest)
			return b.String()
		}
		name := rest[open+1 : open+shut]
		b.WriteString(rest[:open])
		if v, ok := f[name]; ok {
			b.WriteString(isolateStart)
			b.WriteString(v)
			b.WriteString(isolateEnd)
		} else {
			b.WriteString(rest[open : open+shut+1])
		}
		rest = rest[open+shut+1:]
	}
}
