package app

import (
	"strings"
	"testing"
)

func unit(t *testing.T, scope string) ServiceUnit {
	t.Helper()
	return ServiceUnit{Binary: "/usr/local/bin/goalforge", WorkingDir: "/srv/myapp",
		StateDB: "/srv/myapp/.goalforge/goalforge.db", User: "goalforge", Group: "goalforge", Scope: scope}
}

// A worker started from the wrong directory finds no registered project and
// drains an empty queue while looking healthy, so the unit pins where it runs.
func TestUnitPinsWhereTheWorkerRuns(t *testing.T) {
	rendered, err := unit(t, "system").Render()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ExecStart=/usr/local/bin/goalforge worker",
		"WorkingDirectory=/srv/myapp",
		"Environment=GOALFORGE_DB=/srv/myapp/.goalforge/goalforge.db",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("missing %q:\n%s", want, rendered)
		}
	}
}

// A systemd unit is world-readable. A token written into one is published to
// every account on the machine, so secrets go in a file the unit points at.
func TestSecretsAreNotWrittenIntoTheUnit(t *testing.T) {
	u := unit(t, "system")
	u.Environment = map[string]string{"ANTHROPIC_API_KEY": "sk-visible"}
	rendered, err := u.Render()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered, "EnvironmentFile=-/etc/goalforge/worker.env") {
		t.Errorf("the unit must read secrets from a file:\n%s", rendered)
	}
	notes := strings.Join(u.InstallNotes(), "\n")
	for _, secret := range SecretsForService {
		if !strings.Contains(notes, secret) {
			t.Errorf("the notes must name %s as belonging in the credentials file:\n%s", secret, notes)
		}
	}
	if !strings.Contains(notes, "chmod 600") {
		t.Errorf("the notes must say to restrict the credentials file:\n%s", notes)
	}
}

// A worker killed partway through a session leaves a half-applied change
// nobody recorded, so it gets time to stop.
func TestWorkerIsGivenTimeToStop(t *testing.T) {
	rendered, err := unit(t, "system").Render()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"KillSignal=SIGTERM", "TimeoutStopSec=300", "Restart=on-failure"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("missing %q:\n%s", want, rendered)
		}
	}
}

// A user-scope unit cannot set User=, and installs somewhere else.
func TestUserScopeUnitDiffersWhereItMust(t *testing.T) {
	rendered, err := unit(t, "user").Render()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rendered, "User=") {
		t.Errorf("a user unit may not set User=:\n%s", rendered)
	}
	if !strings.Contains(rendered, "WantedBy=default.target") {
		t.Errorf("a user unit installs into default.target:\n%s", rendered)
	}
	if notes := strings.Join(unit(t, "user").InstallNotes(), "\n"); !strings.Contains(notes, "systemctl --user") {
		t.Errorf("the notes must use the user commands:\n%s", notes)
	}
}

// A unit that cannot say where to run is refused rather than emitted with a
// blank that fails at start time.
func TestIncompleteUnitIsRefused(t *testing.T) {
	if _, err := (ServiceUnit{WorkingDir: "/srv"}).Render(); err == nil {
		t.Error("a unit with no binary must be refused")
	}
	if _, err := (ServiceUnit{Binary: "/bin/goalforge"}).Render(); err == nil {
		t.Error("a unit with no working directory must be refused")
	}
}
