# Prometheus Node Collector

Prometheus collector for [prometheus golang client](https://github.com/prometheus/client_golang) 
that collects hardware and OS metrics exposed by \*NIX kernels.

This library is a modification heavily based on [Prometheus Node Exporter](https://github.com/prometheus/node_exporter)

For now, only Ubuntu-based distributions are supported.

# Supported Collectors

- cpu | Exposes CPU statistics
- cpufreq | Exposes CPU frequency statistics
- diskstats | Exposes disk I/O statistics
- filesystem | Exposes filesystem statistics, such as disk space used
- meminfo | Exposes memory statistics
- systemd | Exposes systemd statistics
- thermal_zone | Exposes thermal zone & cooling device statistics from `/sys/class/thermal`

# Usage
```go
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/shadowy-pycoder/go-node-collector/collector"
)

func main() {
	nc, err := collector.NewNodeCollector()
	// By default, it gathers all metrics, but specific collectors can be selected 
	// Example: nc, err := collector.NewNodeCollector("cpu", "systemd")
	if err != nil {
		log.Fatal(err)
	}
	prometheus.DefaultRegisterer.MustRegister(nc)
	port := 9100
	sm := http.NewServeMux()
	sm.Handle("/metrics", promhttp.Handler())
	bindAddress := fmt.Sprintf(":%d", port)
	s := http.Server{
		Addr:         bindAddress,       // configure the bind address
		Handler:      sm,                // set the default handler
		ReadTimeout:  5 * time.Second,   // max time to read request from the client
		WriteTimeout: 10 * time.Second,  // max time to write response to the client
		IdleTimeout:  120 * time.Second, // max time for connections using TCP Keep-Alive
	}

	go func() {
		log.Printf("Starting server on port %d", port)

		err := s.ListenAndServe()
		if err != nil {
			log.Fatal(err)
		}
	}()
	// trap sigterm or interupt and gracefully shutdown the server
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)

	// Block until a signal is received.
	sig := <-c
	log.Println("Got signal:", sig)

	// gracefully shutdown the server, waiting max 30 seconds for current operations to complete
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s.Shutdown(ctx)
}
```