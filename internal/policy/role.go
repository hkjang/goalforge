package policy

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// EnvRole marks a process as running on behalf of an implementation session
// rather than on behalf of the operator. Provider processes and verification
// gates inherit it, so anything they launch — including GoalForge itself —
// knows it is not the operator.
const EnvRole = "GOALFORGE_ROLE"

// Roles.
const (
	// RoleOperator is a person, or their automation, acting with full
	// authority. It is the absence of any marker.
	RoleOperator = "operator"
	// RoleImplementation is a session doing the work. It may change the
	// repository; it may not change what judges the work, who approves it, or
	// what it is allowed to do.
	RoleImplementation = "implementation"
)

// CurrentRole reports the authority this process is running with.
func CurrentRole() string {
	if strings.EqualFold(os.Getenv(EnvRole), RoleImplementation) {
		return RoleImplementation
	}
	return RoleOperator
}

// PrivilegedOperationError explains a refusal in terms of the separation it
// protects rather than as a bare permission failure.
type PrivilegedOperationError struct{ Operation string }

func (e *PrivilegedOperationError) Error() string {
	return fmt.Sprintf("%s 구현 세션이 수행할 수 없습니다: 완료 기준·승인·권한은 구현하는 쪽이 바꿀 수 없습니다", Topic(e.Operation))
}

// RequireOperator refuses an operation that decides whether work is acceptable.
// An implementation session that can change the gates, the approvals, or its
// own budget can manufacture a completion, which is the one thing the
// verification model has to rule out.
func RequireOperator(operation string) error {
	if CurrentRole() == RoleImplementation {
		return &PrivilegedOperationError{Operation: operation}
	}
	return nil
}

// baseEnvKeys are the variables a provider CLI genuinely needs. Everything
// else in the operator's environment — cloud credentials, deployment tokens,
// unrelated API keys — is not something an implementation session should be
// handed just because it happened to be exported.
var baseEnvKeys = []string{
	"PATH", "HOME", "USERPROFILE", "USER", "LOGNAME", "SHELL", "TMPDIR", "TEMP", "TMP",
	"LANG", "LC_ALL", "TERM", "SYSTEMROOT", "COMSPEC", "PATHEXT", "APPDATA", "LOCALAPPDATA",
	"SSL_CERT_FILE", "SSL_CERT_DIR", "NO_PROXY", "HTTP_PROXY", "HTTPS_PROXY",
}

// providerEnvPrefixes are the credential families a provider CLI authenticates
// with. They are passed through because without them the tool cannot run at
// all; everything outside them is withheld.
var providerEnvPrefixes = []string{
	"ANTHROPIC_", "CLAUDE_", "OPENAI_", "CODEX_", "QWEN_", "OPENCODE_", "XDG_", "GOALFORGE_",
}

// secretEnvKeys never reach an execution session, whatever else is allowed.
// They are the credentials that would let a session act as the operator over
// it: the audit key would let it recompute the integrity chain it is the
// subject of, the API and MCP tokens would let it call the management surface
// as an authenticated client, and the database DSN carries a password.
//
// Prefix allowances do not cover these, and neither does GOALFORGE_INHERIT_ENV=all.
// An operator widening the environment is saying "this session may see my
// tooling", not "this session may hold the keys to its own audit".
var secretEnvKeys = map[string]bool{
	"GOALFORGE_AUDIT_KEY":    true,
	"GOALFORGE_API_TOKEN":    true,
	"GOALFORGE_MCP_TOKEN":    true,
	"GOALFORGE_POSTGRES_DSN": true,
}

// SecretEnvKeys lists the variables withheld from every session.
func SecretEnvKeys() []string {
	keys := make([]string, 0, len(secretEnvKeys))
	for key := range secretEnvKeys {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// EnvInherit and EnvPassthrough let an operator widen the set deliberately.
const (
	EnvInherit     = "GOALFORGE_INHERIT_ENV"
	EnvPassthrough = "GOALFORGE_PASS_ENV"
)

// SessionEnvironment builds the environment an implementation session runs
// with: a named set rather than whatever the operator happened to export. The
// role marker is always added, and `GOALFORGE_INHERIT_ENV=all` restores the
// old inherit-everything behaviour for anyone who needs it.
func SessionEnvironment(host []string, role string) []string {
	if role == "" {
		role = RoleImplementation
	}
	if strings.EqualFold(os.Getenv(EnvInherit), "all") {
		return append(withoutSecrets(host), EnvRole+"="+role)
	}
	allowed := map[string]bool{}
	for _, key := range baseEnvKeys {
		allowed[key] = true
	}
	for _, key := range strings.Split(os.Getenv(EnvPassthrough), ",") {
		if trimmed := strings.TrimSpace(key); trimmed != "" {
			allowed[trimmed] = true
		}
	}
	var filtered []string
	for _, entry := range host {
		key, _, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		if secretEnvKeys[key] {
			continue
		}
		if allowed[key] || hasAnyPrefix(key, providerEnvPrefixes) {
			filtered = append(filtered, entry)
		}
	}
	sort.Strings(filtered)
	return append(filtered, EnvRole+"="+role)
}

// withoutSecrets strips the credentials no session may hold, so the widest
// inheritance setting still cannot hand them over.
func withoutSecrets(host []string) []string {
	filtered := make([]string, 0, len(host))
	for _, entry := range host {
		key, _, found := strings.Cut(entry, "=")
		if found && secretEnvKeys[key] {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func hasAnyPrefix(value string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}
