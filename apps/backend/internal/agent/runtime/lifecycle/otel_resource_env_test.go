package lifecycle

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestApplyKandevOTelResourceAttributes_AddsLaunchIdentity(t *testing.T) {
	env := map[string]string{}
	applyKandevOTelResourceAttributes(env, &LaunchRequest{
		WorkspaceID: "ws-1", TaskID: "task-1", SessionID: "session-1",
		AgentProfileID: "profile-1", ExecutorType: "ssh",
	})

	require.Equal(t,
		"kandev.workspace_id=ws-1,kandev.task_id=task-1,kandev.session_id=session-1,"+
			"kandev.agent_profile_id=profile-1,kandev.execution_profile_id=profile-1,kandev.executor_type=ssh",
		env[envKeyOTelResourceAttributes])
}

func TestApplyKandevOTelResourceAttributes_KeepsForeignAndReplacesStaleKandevKeys(t *testing.T) {
	env := map[string]string{
		envKeyOTelResourceAttributes: " team=platform , kandev.task_id=stale,,seat=mini-3 ",
	}
	applyKandevOTelResourceAttributes(env, &LaunchRequest{TaskID: "task-2"})

	require.Equal(t, "team=platform,seat=mini-3,kandev.task_id=task-2", env[envKeyOTelResourceAttributes])
}

func TestApplyKandevOTelResourceAttributes_EscapesReservedCharacters(t *testing.T) {
	env := map[string]string{}
	applyKandevOTelResourceAttributes(env, &LaunchRequest{TaskID: "a,b=c d"})

	require.Equal(t, "kandev.task_id=a%2Cb%3Dc%20d", env[envKeyOTelResourceAttributes])
}

func TestApplyKandevOTelResourceAttributes_RemovesValueLeftWithOnlyStaleKandevKeys(t *testing.T) {
	env := map[string]string{envKeyOTelResourceAttributes: "kandev.task_id=stale"}
	applyKandevOTelResourceAttributes(env, &LaunchRequest{})

	_, present := env[envKeyOTelResourceAttributes]
	require.False(t, present)
}

func TestApplyKandevOTelResourceAttributes_NilInputsAreNoOps(t *testing.T) {
	applyKandevOTelResourceAttributes(nil, &LaunchRequest{TaskID: "task-1"})
	env := map[string]string{"KEEP": "1"}
	applyKandevOTelResourceAttributes(env, nil)
	require.Equal(t, map[string]string{"KEEP": "1"}, env)
}

func TestBuildEnvForExecution_SetsKandevOTelResourceAttributes(t *testing.T) {
	mgr := newTestManager(t)
	env, err := mgr.buildEnvForExecution(context.Background(), "exec-1", &LaunchRequest{
		TaskID: "task-1", SessionID: "session-1", AgentProfileID: "profile-1",
		Env: map[string]string{envKeyOTelResourceAttributes: "team=platform"},
	}, nil, nil)

	require.NoError(t, err)
	require.Equal(t,
		"team=platform,kandev.task_id=task-1,kandev.session_id=session-1,"+
			"kandev.agent_profile_id=profile-1,kandev.execution_profile_id=profile-1",
		env[envKeyOTelResourceAttributes])
}

func TestBuildEnvForExecution_FinalizedEnvironmentGetsKandevOTelResourceAttributes(t *testing.T) {
	mgr := newTestManager(t)
	original := map[string]string{"KEEP": "1"}
	env, err := mgr.buildEnvForExecution(context.Background(), "exec-1", &LaunchRequest{
		TaskID: "task-1", EnvironmentFinalized: true, Env: original,
	}, nil, nil)

	require.NoError(t, err)
	require.Equal(t, "kandev.task_id=task-1", env[envKeyOTelResourceAttributes])
	require.NotContains(t, original, envKeyOTelResourceAttributes, "the request env must not be mutated")
}

func TestSSHRemoteAgentEnv_ForwardsOTelResourceAttributes(t *testing.T) {
	got := sshRemoteAgentEnv(&ExecutorCreateRequest{Env: map[string]string{
		envKeyOTelResourceAttributes: "kandev.task_id=task-1",
		"UNRELATED_PROFILE_VAR":      "not-forwarded",
	}})

	require.Equal(t, "kandev.task_id=task-1", got[envKeyOTelResourceAttributes])
	require.NotContains(t, got, "UNRELATED_PROFILE_VAR")
}
