package audit

import (
	"strings"
	"testing"
)

// The digest must bind the fields it covers, or a change that moves text
// across a boundary would leave the digest unchanged.
func TestPayloadDigestBindsFieldBoundaries(t *testing.T) {
	if PayloadDigest("ab", "c") == PayloadDigest("a", "bc") {
		t.Fatal("moving a character across a field boundary must change the digest")
	}
	if PayloadDigest("x", "y") != PayloadDigest("x", "y") {
		t.Fatal("the same values must digest the same")
	}
}

// Every link depends on the one before it, which is what makes a change
// anywhere in the history detectable from that point on.
func TestLinkDigestDependsOnTheWholeChain(t *testing.T) {
	first := LinkDigest("", "payload", "evidence", "R1", "t0")
	branched := LinkDigest("different", "payload", "evidence", "R1", "t0")
	if first == branched {
		t.Fatal("a different predecessor must produce a different link")
	}
	for name, changed := range map[string]string{
		"payload": LinkDigest("", "other", "evidence", "R1", "t0"),
		"kind":    LinkDigest("", "payload", "approval", "R1", "t0"),
		"record":  LinkDigest("", "payload", "evidence", "R2", "t0"),
		"time":    LinkDigest("", "payload", "evidence", "R1", "t1"),
	} {
		if changed == first {
			t.Errorf("changing the %s must change the link", name)
		}
	}
}

// With a key the links are MACs, so someone who can write the database still
// cannot recompute the chain. Without one they can, and the product has to say
// which is in force rather than implying the stronger claim.
func TestKeyChangesTheLinksAndIsReported(t *testing.T) {
	unkeyed := LinkDigest("", "payload", "evidence", "R1", "t0")
	if Keyed() {
		t.Fatal("no key is configured in this test's environment")
	}
	t.Setenv(EnvChainKey, "secret")
	if !Keyed() {
		t.Fatal("a configured key must be reported")
	}
	keyed := LinkDigest("", "payload", "evidence", "R1", "t0")
	if keyed == unkeyed {
		t.Fatal("a keyed chain must not be reproducible without the key")
	}
	t.Setenv(EnvChainKey, "different-secret")
	if LinkDigest("", "payload", "evidence", "R1", "t0") == keyed {
		t.Fatal("a different key must produce different links")
	}
	if len(keyed) != 64 || strings.TrimSpace(keyed) == "" {
		t.Fatalf("unexpected digest shape: %q", keyed)
	}
}
