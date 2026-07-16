package audit

import (
	"os"
	"regexp"
	"sort"
	"strings"
)

const Mask = "[REDACTED]"

var secretAssignment = regexp.MustCompile(`(?i)((?:authorization|api[_-]?key|access[_-]?token|refresh[_-]?token|password|secret|credential)\s*["']?\s*[:=]\s*["']?(?:bearer\s+)?)([^\s,"']+)`)
var knownToken = regexp.MustCompile(`\b(?:sk|ghp|github_pat|xox[baprs])[-_][A-Za-z0-9_-]{8,}\b`)
var sensitiveName = regexp.MustCompile(`(?i)(TOKEN|SECRET|PASSWORD|PASSWD|API_KEY|APIKEY|CREDENTIAL|AUTH)`)

func RedactString(value string) string {
	value = redactEnvironmentValues(value)
	value = secretAssignment.ReplaceAllString(value, `${1}`+Mask)
	return knownToken.ReplaceAllString(value, Mask)
}

func RedactBytes(value []byte) []byte { return []byte(RedactString(string(value))) }

func redactEnvironmentValues(value string) string {
	secrets := make([]string, 0)
	for _, entry := range os.Environ() {
		name, secret, found := strings.Cut(entry, "=")
		if found && sensitiveName.MatchString(name) && len(secret) >= 8 {
			secrets = append(secrets, secret)
		}
	}
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	for _, secret := range secrets {
		value = strings.ReplaceAll(value, secret, Mask)
	}
	return value
}
