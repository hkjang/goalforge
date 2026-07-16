package audit

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestRedactStringMasksAssignmentsTokensAndEnvironment(t *testing.T) {
	t.Setenv("SERVICE_API_KEY", "environment-secret-value")
	input := `Authorization: Bearer abcdefghijkl api_key="another-secret" token=ghp_1234567890abcdef environment-secret-value`
	got := RedactString(input)
	for _, secret := range []string{"abcdefghijkl", "another-secret", "ghp_1234567890abcdef", "environment-secret-value"} {
		if strings.Contains(got, secret) {
			t.Fatalf("secret %q remained in %q", secret, got)
		}
	}
}

func TestEncryptFromEnvironmentIsOptionalAndEncrypted(t *testing.T) {
	t.Setenv("GOALFORGE_AUDIT_KEY", "")
	if encrypted, err := EncryptFromEnvironment([]byte("secret")); err != nil || encrypted != nil {
		t.Fatalf("encrypted=%v err=%v", encrypted, err)
	}
	key := []byte("0123456789abcdef0123456789abcdef")
	t.Setenv("GOALFORGE_AUDIT_KEY", base64.StdEncoding.EncodeToString(key))
	encrypted, err := EncryptFromEnvironment([]byte("secret"))
	if err != nil || string(encrypted) == "secret" || len(encrypted) <= len("secret") {
		t.Fatalf("encrypted=%x err=%v", encrypted, err)
	}
}
