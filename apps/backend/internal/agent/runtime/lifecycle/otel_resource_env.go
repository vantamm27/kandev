package lifecycle

import "strings"

// envKeyOTelResourceAttributes is the OpenTelemetry SDK variable that agents
// such as Claude Code read for extra resource attributes on their telemetry.
const envKeyOTelResourceAttributes = "OTEL_RESOURCE_ATTRIBUTES"

// kandevOTelAttributePrefix namespaces the attributes Kandev owns inside
// OTEL_RESOURCE_ATTRIBUTES.
const kandevOTelAttributePrefix = "kandev."

// applyKandevOTelResourceAttributes appends the launch's Kandev identity to
// OTEL_RESOURCE_ATTRIBUTES so agent telemetry joins back to its workspace,
// task, and session. Attributes from the profile or request are kept; any
// kandev.* attribute already present is replaced by the launch value.
func applyKandevOTelResourceAttributes(env map[string]string, req *LaunchRequest) {
	if env == nil || req == nil {
		return
	}
	identity := []struct{ key, value string }{
		{"kandev.workspace_id", req.WorkspaceID},
		{"kandev.task_id", req.TaskID},
		{"kandev.session_id", req.SessionID},
		{"kandev.agent_profile_id", req.AgentProfileID},
		{"kandev.execution_profile_id", executionProfileID(req)},
		{"kandev.executor_type", req.ExecutorType},
	}
	pairs := foreignOTelResourceAttributes(env[envKeyOTelResourceAttributes])
	for _, attr := range identity {
		if attr.value != "" {
			pairs = append(pairs, attr.key+"="+escapeOTelResourceValue(attr.value))
		}
	}
	if len(pairs) == 0 {
		delete(env, envKeyOTelResourceAttributes)
		return
	}
	env[envKeyOTelResourceAttributes] = strings.Join(pairs, ",")
}

// foreignOTelResourceAttributes returns the non-empty key=value entries of an
// OTEL_RESOURCE_ATTRIBUTES value whose keys are not owned by Kandev.
func foreignOTelResourceAttributes(value string) []string {
	var kept []string
	for _, entry := range strings.Split(value, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(strings.TrimSpace(key), kandevOTelAttributePrefix) {
			continue
		}
		kept = append(kept, entry)
	}
	return kept
}

// escapeOTelResourceValue percent-encodes every byte outside the RFC 3986
// unreserved set, as the W3C Baggage format used by OTEL_RESOURCE_ATTRIBUTES
// requires for ',', '=', spaces, and non-ASCII characters.
func escapeOTelResourceValue(value string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(value); i++ {
		c := value[i]
		if isOTelUnreserved(c) {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&0x0F])
	}
	return b.String()
}

func isOTelUnreserved(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
		c == '-' || c == '_' || c == '.' || c == '~'
}
