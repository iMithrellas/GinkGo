package imagecontext

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type runnerFunc func(context.Context, string, []string, int) ([]byte, error)

func (f runnerFunc) Run(ctx context.Context, executable string, args []string, limit int) ([]byte, error) {
	return f(ctx, executable, args, limit)
}

func TestHyprlandTargets(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mode   Mode
		window string
		want   [][]string
	}{
		{"window negative coordinates", Window, `{"address":"0x1","monitor":2,"at":[-1700,50],"size":[1400,900]}`, [][]string{{"-g", "-1700,50 1400x900", "-"}}},
		{"window monitor wins over focused monitor", Screen, `{"address":"0x1","monitor":2}`, [][]string{{"-o", "DP-2", "-"}}},
		{"empty desktop uses focused monitor", Screen, `{}`, [][]string{{"-o", "DP-1", "-"}}},
		{"all outputs separately", AllScreens, `{}`, [][]string{{"-o", "DP-1", "-"}, {"-o", "DP-2", "-"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var captures [][]string
			data := testPNG(t)
			h := NewHyprland(runnerFunc(func(_ context.Context, executable string, args []string, _ int) ([]byte, error) {
				if executable == "grim" {
					captures = append(captures, args)
					return data, nil
				}
				if args[1] == "activewindow" {
					return []byte(tc.window), nil
				}
				return []byte(`[{"id":1,"name":"DP-1","focused":true,"scale":1.5},{"id":2,"name":"DP-2","scale":2},{"id":3,"name":"OFF","disabled":true}]`), nil
			}))
			images, err := h.Capture(context.Background(), tc.mode)
			require.NoError(t, err)
			require.Equal(t, tc.want, captures)
			require.Len(t, images, len(tc.want))
		})
	}
}

func TestHyprlandMissingTargetAndCancellation(t *testing.T) {
	h := NewHyprland(runnerFunc(func(ctx context.Context, executable string, _ []string, _ int) ([]byte, error) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		require.Equal(t, "hyprctl", executable, "must not capture a broader target")
		return []byte(`{}`), nil
	}))
	_, err := h.Capture(context.Background(), Window)
	require.ErrorContains(t, err, "no focused window")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = h.Capture(ctx, Screen)
	require.True(t, errors.Is(err, context.Canceled))
}
