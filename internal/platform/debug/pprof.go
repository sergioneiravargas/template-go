package debug

import (
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"os"
)

func StartPProfServer(addr string) {
	go func() {
		server := &http.Server{
			Addr:    addr,
			Handler: http.DefaultServeMux,
		}
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "pprof server error: %v\n", err)
		}
	}()
}
