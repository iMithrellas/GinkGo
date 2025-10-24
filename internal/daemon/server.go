package daemon

import (
    "context"
    "fmt"
    "net"
    "net/http"
)

// Main provides a standalone daemon entrypoint.
func Main() error {
    // Wireframe: run a basic HTTP server for health checks.
    mux := http.NewServeMux()
    mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
        _, _ = w.Write([]byte("ok"))
    })
    srv := &http.Server{Addr: ":7465", Handler: mux}
    return srv.ListenAndServe()
}

// Start launches the daemon in-process (used by tests or CLI control).
func Start(ctx context.Context, l net.Listener) error {
    mux := http.NewServeMux()
    mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
        _, _ = fmt.Fprint(w, "ok")
    })
    srv := &http.Server{Handler: mux}
    go func() {
        <-ctx.Done()
        _ = srv.Shutdown(context.Background())
    }()
    return srv.Serve(l)
}

