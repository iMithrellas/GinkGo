package daemon

import (
	"context"
	"fmt"

	"github.com/mithrel/ginkgo/internal/config"
	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/mithrel/ginkgo/internal/voiceevents"
)

// autostartVoice is called once after the IPC listener binds. A manual stop is
// final for this daemon lifetime; only a new explicit request restarts capture.
func (v *voiceRuntime) autostartVoice(ctx context.Context) error {
	settings := v.sherpaSettings
	if settings == nil || !settings.GetBool("voice.autostart") {
		return nil
	}
	if !v.enabled || !v.streamEnabled || v.events == nil || config.VoiceStreamBackend(settings) != "sherpa" {
		return fmt.Errorf("voice autostart requires enabled Sherpa streaming and events")
	}
	namespace := settings.GetString("namespace")
	if namespace == "" {
		namespace = "default"
	}
	response := v.stream(ctx, ipc.Message{Namespace: namespace, Save: true, Mode: settings.GetString("voice.mode")})
	if response.OK {
		return nil
	}
	v.mu.Lock()
	v.status.State, v.status.LastError = "error", response.Msg
	v.mu.Unlock()
	_ = v.events.Publish(voiceevents.Event{Type: "error", Error: response.Msg})
	return fmt.Errorf("voice autostart: %s", response.Msg)
}
