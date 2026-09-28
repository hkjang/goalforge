package audit

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
)

// EnvChainKey names the secret that makes the integrity chain unforgeable.
// It is deliberately never passed into an execution session: the point of the
// chain is that the thing being audited cannot rewrite the audit.
const EnvChainKey = "GOALFORGE_AUDIT_KEY"

// Keyed reports whether a chain key is configured.
//
// This distinction is the honest part of the feature. Without a key the chain
// is a plain hash chain: it detects a record edited or deleted in place, and
// a record inserted behind its back, which is what accidental corruption and
// a careless edit look like. It does not stop someone with write access to the
// database from recomputing every link. With a key they cannot, because they
// cannot produce the MACs. Claiming tamper-proofing without saying which of
// these is in force would be the same kind of overstatement the chain exists
// to catch.
func Keyed() bool { return strings.TrimSpace(os.Getenv(EnvChainKey)) != "" }

// PayloadDigest hashes the fields of a record that must not change. It is
// computed from the values themselves, so a later edit to the row is
// detectable by recomputing it and finding it no longer matches.
func PayloadDigest(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		// Length-prefixed so that moving a character across a field boundary
		// cannot produce the same digest.
		h.Write([]byte(itoa(len(part))))
		h.Write([]byte{0})
		h.Write([]byte(part))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// LinkDigest binds one entry to the one before it. Changing, inserting, or
// removing any entry breaks every link after it.
func LinkDigest(previous, payloadDigest, kind, recordID, recordedAt string) string {
	material := strings.Join([]string{previous, payloadDigest, kind, recordID, recordedAt}, "\x00")
	if key := strings.TrimSpace(os.Getenv(EnvChainKey)); key != "" {
		mac := hmac.New(sha256.New, []byte(key))
		mac.Write([]byte(material))
		return hex.EncodeToString(mac.Sum(nil))
	}
	sum := sha256.Sum256([]byte(material))
	return hex.EncodeToString(sum[:])
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
