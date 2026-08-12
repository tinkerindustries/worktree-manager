package coord

// redact.go is one of the security pass's fixes (phase 9): an identity key
// is itself a credential. A named client's key IS its token — the thing
// that authenticates it — and an ephemeral client's key is the session id
// its entries are owned under. Serving those keys to any other client would
// hand that client the identity: it could present the token, pass the
// ownership check, and read the owner's seed credentials with `wt list
// --wide`. The registry itself keeps the full keys (the store is 0600 and
// the coordinator's alone); every rendering for a reader who is not the
// identity shows the redacted form instead.

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/mrgeoffrich/worktree-manager/internal/protocol"
)

// redactKey renders an identity key for a reader that is not the identity:
// a short hash of the key — enough to tell two foreign entries apart in a
// listing, not enough to recover the key (48 bits of a 256-bit token are
// nothing). A host key is the uid, which is not secret, and is passed
// through unchanged.
func redactKey(kind, key string) string {
	if kind != protocol.KindNamed && kind != protocol.KindEphemeral {
		return key
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:6])
}
