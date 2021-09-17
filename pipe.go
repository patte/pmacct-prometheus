package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// {"event_type": "purge", "ip_src": "10.0.1.1", "ip_dst": "10.0.2.1", "packets": 2, "bytes": 143}
type flow struct {
	IpSrc    string `json:"ip_src"`
	IpDst    string `json:"ip_dst"`
	Packages int    `json:"packets"`
	Bytes    int    `json:"bytes"`
}

var (
	addr = flag.String("addr", ":9590", "Listening Address for /metrics")
)

var (
	flowBytesTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "flow_bytes_total",
			Help: "Flow Bytes.",
		},
		[]string{"ip_src", "ip_dst"},
	)
	flowPackagesTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "flow_packages_total",
			Help: "Flow Packages.",
		},
		[]string{"ip_src", "ip_dst"},
	)
)

func main() {
	flag.Parse()

	go func() {
		log.Printf("Starting Prometheus web server, available at: http://%s/metrics\n", *addr)
		http.Handle("/metrics", promhttp.Handler())
		http.ListenAndServe(*addr, nil)
	}()

	cmd := exec.Command("pmacctd", "-r 1", "-c src_host,dst_host", "-P print", "-O json")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		log.Fatalf("cmd.Start() failed with '%s'\n", err)
	}

	var wg sync.WaitGroup
	wg.Add(1)

	// listen to SIGINT, SIGTERM
	go func() {
		termChan := make(chan os.Signal)
		signal.Notify(termChan, syscall.SIGINT, syscall.SIGTERM)
		<-termChan // blocks
		fmt.Println("term received, shutting down...")
		wg.Done()
	}()

	scanner := bufio.NewScanner(stdout)
	go func() {
		for scanner.Scan() {
			text := scanner.Text()
			if strings.HasPrefix(text, "{") {
				var f flow
				if err := json.Unmarshal([]byte(text), &f); err != nil {
					log.Fatal(err)
				}
				fmt.Printf("%+v\n", f)
				flowBytesTotal.With(
					prometheus.Labels{
						"ip_src": f.IpSrc,
						"ip_dst": f.IpDst,
					},
				).Add(float64(f.Bytes))
				flowPackagesTotal.With(
					prometheus.Labels{
						"ip_src": f.IpSrc,
						"ip_dst": f.IpDst,
					},
				).Add(float64(f.Packages))
			} else {
				fmt.Println(text)
			}
			// wg.Done()
		}
	}()

	wg.Wait()

	// send SIGINT to pmacctd
	err = cmd.Process.Signal(syscall.SIGINT)
	if err != nil {
		log.Fatal(err)
	}

	// wait for pmacctd to exit
	if err := cmd.Wait(); err != nil {
		log.Fatal(err)
	}

	fmt.Println("finished!")
}
