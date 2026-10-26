package agent

import (
	"strings"
	"testing"
)

func TestSystemdUnitRunsTheAgentWithItsConfig(t *testing.T) {
	unit, err := SystemdUnit("/home/seat/.local/bin/fleet-agent", "/home/seat/.config/fleet-agent/config.json")
	if err != nil {
		t.Fatal(err)
	}
	text := string(unit)
	for _, want := range []string{
		"[Service]\n",
		`ExecStart="/home/seat/.local/bin/fleet-agent" "run" "--config" "/home/seat/.config/fleet-agent/config.json"` + "\n",
		"Restart=always\n",
		"WantedBy=default.target\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("unit lacks %q:\n%s", want, text)
		}
	}
}

func TestSystemdUnitEscapesSpecialCharacters(t *testing.T) {
	unit, err := SystemdUnit(`/opt/my agent/fleet-agent`, `/srv/100%/"cfg"\x.json`)
	if err != nil {
		t.Fatal(err)
	}
	if want := `ExecStart="/opt/my agent/fleet-agent" "run" "--config" "/srv/100%%/\"cfg\"\\x.json"`; !strings.Contains(string(unit), want) {
		t.Fatalf("unit = %s\nwant line %s", unit, want)
	}
	if _, err := SystemdUnit("/bin/fleet-agent", "/tmp/a\nb"); err == nil {
		t.Fatal("a newline in a path must be rejected")
	}
}
