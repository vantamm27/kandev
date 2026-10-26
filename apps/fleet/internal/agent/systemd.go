package agent

import (
	"bytes"
	"errors"
	"strings"
)

// SystemdUnitName is the systemd user unit for the seat's fleet-agent.
const SystemdUnitName = "fleet-agent.service"

// SystemdUnit renders a systemd user unit that keeps `fleet-agent run`
// alive on a Linux seat. Logs go to the user journal.
func SystemdUnit(binary, configPath string) ([]byte, error) {
	var exec []string
	for _, arg := range []string{binary, "run", "--config", configPath} {
		quoted, err := systemdQuote(arg)
		if err != nil {
			return nil, err
		}
		exec = append(exec, quoted)
	}
	var b bytes.Buffer
	b.WriteString("[Unit]\nDescription=Kandev fleet agent\nWants=network-online.target\nAfter=network-online.target\n\n")
	b.WriteString("[Service]\nExecStart=" + strings.Join(exec, " ") + "\n")
	b.WriteString("Restart=always\nRestartSec=10\n\n[Install]\nWantedBy=default.target\n")
	return b.Bytes(), nil
}

// systemdQuote double-quotes one ExecStart argument, escaping what systemd
// would otherwise interpret: backslashes, quotes, and % specifiers.
func systemdQuote(arg string) (string, error) {
	if strings.ContainsAny(arg, "\n\r") {
		return "", errors.New("systemd unit argument contains a newline")
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%")
	return `"` + r.Replace(arg) + `"`, nil
}
