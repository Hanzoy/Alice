package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"time"

	"alice/internal/componenthost"
)

func main() {
	addr := flag.String("addr", ":8090", "listen address")
	source := flag.String("components", "components", "component source root")
	data := flag.String("data", "data/component-host", "component host data")
	flag.Parse()
	server, err := componenthost.New(*source, *data, os.Getenv("ALICE_COMPONENT_HOST_TOKEN"))
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("Alice Component Host listening on %s", *addr)
	log.Fatal((&http.Server{Addr: *addr, Handler: server.Handler(), ReadHeaderTimeout: 10 * time.Second}).ListenAndServe())
}
