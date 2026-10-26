package agent

import (
	"bytes"
	"encoding/xml"
	"fmt"
)

// LaunchAgentLabel is the launchd label for the seat's fleet-agent.
const LaunchAgentLabel = "com.kandev.fleet-agent"

// LaunchAgentPlist renders a per-user LaunchAgent that runs
// `fleet-agent run --config <configPath>` at login and restarts it if it exits.
func LaunchAgentPlist(binary, configPath, logPath string) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString(xml.Header)
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString("<plist version=\"1.0\">\n<dict>\n")
	pairs := []struct {
		key   string
		value string
	}{
		{"Label", LaunchAgentLabel},
		{"StandardOutPath", logPath},
		{"StandardErrorPath", logPath},
	}
	for _, p := range pairs {
		if err := writeKeyString(&b, p.key, p.value); err != nil {
			return nil, err
		}
	}
	b.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n")
	for _, arg := range []string{binary, "run", "--config", configPath} {
		if err := writeElement(&b, "\t\t", "string", arg); err != nil {
			return nil, err
		}
	}
	b.WriteString("\t</array>\n\t<key>RunAtLoad</key>\n\t<true/>\n\t<key>KeepAlive</key>\n\t<true/>\n")
	b.WriteString("\t<key>ThrottleInterval</key>\n\t<integer>10</integer>\n</dict>\n</plist>\n")
	return b.Bytes(), nil
}

func writeKeyString(b *bytes.Buffer, key, value string) error {
	if err := writeElement(b, "\t", "key", key); err != nil {
		return err
	}
	return writeElement(b, "\t", "string", value)
}

func writeElement(b *bytes.Buffer, indent, tag, value string) error {
	b.WriteString(indent + "<" + tag + ">")
	if err := xml.EscapeText(b, []byte(value)); err != nil {
		return fmt.Errorf("escape plist value: %w", err)
	}
	b.WriteString("</" + tag + ">\n")
	return nil
}
