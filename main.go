package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"apk_poc_ms/routines"
	httpServer "apk_poc_ms/routines/http_server"
)

func main() {
	registry := map[string]routines.Routine{
		"http_server": httpServer.NewHTTPServer(),
	}

	for name, routine := range registry {
		if err := routine.Start(); err != nil {
			log.Fatalf("failed to start %s: %v", name, err)
		}
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	for name, routine := range registry {
		if err := routine.Stop(); err != nil {
			log.Printf("failed to stop %s: %v", name, err)
		}
	}

	signal.Stop(sigCh)
}
