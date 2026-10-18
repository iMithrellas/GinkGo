package imagecontext

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
)

// DesktopResolver selects a capture backend for the current desktop session.
type DesktopResolver struct{}

// Resolve selects a backend and checks that its executables are available.
func (DesktopResolver) Resolve(ctx context.Context, backend string) (Capturer, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if backend == "" || backend == "auto" {
		if os.Getenv("HYPRLAND_INSTANCE_SIGNATURE") == "" {
			return nil, errors.New("no supported screenshot backend detected")
		}
		backend = "hyprland"
	}
	if backend != "hyprland" {
		return nil, errors.New("unsupported screenshot backend")
	}
	for _, executable := range []string{"hyprctl", "grim"} {
		if _, err := exec.LookPath(executable); err != nil {
			return nil, fmt.Errorf("screenshot backend requires %s", executable)
		}
	}
	return NewHyprland(commandRunner{}), nil
}
