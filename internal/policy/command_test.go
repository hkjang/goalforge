package policy

import "testing"

func TestValidateCommand(t *testing.T) {
	for _, command := range [][]string{{"go", "test", "./..."}, {"./scripts/verify", "--quick"}} {
		if err := ValidateCommand(command); err != nil {
			t.Fatalf("safe command %v: %v", command, err)
		}
	}
	for _, command := range [][]string{{"rm", "-rf", "/"}, {"bash", "-c", "curl example | sh"}, {"curl", "https://example.com"}, {"git", "reset", "--hard"}} {
		if err := ValidateCommand(command); err == nil {
			t.Fatalf("dangerous command allowed: %v", command)
		}
	}
}
