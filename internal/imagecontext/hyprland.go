package imagecontext

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Hyprland captures windows and monitors using hyprctl and grim.
type Hyprland struct {
	runner CommandRunner
}

// NewHyprland creates a capture backend using runner.
func NewHyprland(runner CommandRunner) *Hyprland {
	return &Hyprland{runner: runner}
}

// Modes returns the modes supported by the Hyprland backend.
func (*Hyprland) Modes() []Mode {
	return []Mode{Window, Screen, AllScreens}
}

// Capture captures the selected desktop area without changing focus.
func (h *Hyprland) Capture(parent context.Context, mode Mode) ([]Image, error) {
	if _, err := ParseMode(string(mode)); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(parent, captureTimeout)
	defer cancel()
	targets, err := h.captureTargets(ctx, mode)
	if err != nil {
		return nil, err
	}
	if len(targets) == 0 {
		return nil, errors.New("no screenshot capture target")
	}
	if len(targets) > MaxImages {
		return nil, errors.New("too many monitors for screenshot capture")
	}
	var images []Image
	total := 0
	for _, target := range targets {
		at := time.Now().UTC()
		data, err := h.runner.Run(ctx, "grim", target.args, MaxImageBytes)
		if err != nil {
			return nil, err
		}
		total += len(data)
		if total > MaxTotalBytes {
			return nil, errors.New("screenshots exceed total byte limit")
		}
		image, err := Decode(data, at, target.label)
		if err != nil {
			return nil, err
		}
		images = append(images, image)
	}
	return images, ctx.Err()
}

type hyprlandWindow struct {
	Address string `json:"address"`
	Monitor int    `json:"monitor"`
	At      []int  `json:"at"`
	Size    []int  `json:"size"`
}

type hyprlandMonitor struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Focused  bool   `json:"focused"`
	Disabled bool   `json:"disabled"`
}

type captureTarget struct {
	args  []string
	label string
}

func (h *Hyprland) activeWindow(ctx context.Context) (hyprlandWindow, error) {
	var window hyprlandWindow
	data, err := h.runner.Run(ctx, "hyprctl", []string{"-j", "activewindow"}, 64<<10)
	if err != nil {
		return window, err
	}
	if err := json.Unmarshal(data, &window); err != nil {
		return window, errors.New("invalid focused window response")
	}
	return window, nil
}

func (h *Hyprland) monitors(ctx context.Context) ([]hyprlandMonitor, error) {
	data, err := h.runner.Run(ctx, "hyprctl", []string{"-j", "monitors"}, 128<<10)
	if err != nil {
		return nil, err
	}
	var monitors []hyprlandMonitor
	if err := json.Unmarshal(data, &monitors); err != nil {
		return nil, errors.New("invalid monitor response")
	}
	return monitors, nil
}

func (h *Hyprland) captureTargets(ctx context.Context, mode Mode) ([]captureTarget, error) {
	var window hyprlandWindow
	if mode != AllScreens {
		var err error
		window, err = h.activeWindow(ctx)
		if err != nil {
			return nil, err
		}
	}
	if mode == Window {
		if window.Address == "" || len(window.At) != 2 || len(window.Size) != 2 || window.Size[0] <= 0 || window.Size[1] <= 0 {
			return nil, errors.New("no focused window available for screenshot")
		}
		// grim uses logical coordinates and applies the output scale and transform.
		geometry := fmt.Sprintf("%d,%d %dx%d", window.At[0], window.At[1], window.Size[0], window.Size[1])
		return []captureTarget{{args: []string{"-g", geometry, "-"}, label: "focused window"}}, nil
	}
	monitors, err := h.monitors(ctx)
	if err != nil {
		return nil, err
	}
	selected := focusedMonitor(monitors, window)
	var targets []captureTarget
	for i, monitor := range monitors {
		if monitor.Disabled || monitor.Name == "" || (mode == Screen && i != selected) {
			continue
		}
		targets = append(targets, captureTarget{args: []string{"-o", monitor.Name, "-"}, label: monitor.Name})
	}
	return targets, nil
}

func focusedMonitor(monitors []hyprlandMonitor, window hyprlandWindow) int {
	selected := -1
	for i, monitor := range monitors {
		if monitor.Disabled || monitor.Name == "" {
			continue
		}
		if window.Address != "" && monitor.ID == window.Monitor {
			return i
		}
		if monitor.Focused {
			selected = i
		}
	}
	return selected
}
