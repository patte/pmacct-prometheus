package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/oschwald/geoip2-golang"
)

// {"event_type": "purge", "ip_src": "10.0.1.1", "ip_dst": "10.0.2.1", "packets": 2, "bytes": 143}
type Peer struct {
	Ip      net.IP
	Country string
	City    string
	Asn     string
}
type flow struct {
	IpSrc    string `json:"ip_src"`
	IpDst    string `json:"ip_dst"`
	Packages int    `json:"packets"`
	Bytes    int    `json:"bytes"`
}

var (
	addr    = flag.String("addr", ":9590", "Listening Address for /metrics")
	verbose = flag.Bool("verbose", false, "Be chatty on stdout")
)

var (
	flowBytesTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "flow_bytes_total",
			Help: "Flow Bytes.",
		},
		[]string{"ip_src", "ip_dst", "country_src", "country_dst", "asn_src", "asn_dst"},
	)
	flowPackagesTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "flow_packages_total",
			Help: "Flow Packages.",
		},
		[]string{"ip_src", "ip_dst"},
	)
)

func MakePeer(ipRaw string, dbCity *geoip2.Reader, dbASN *geoip2.Reader) (*Peer, error) {
	ip := net.ParseIP(ipRaw)
	if ip == nil {
		return nil, errors.New("ip invalid")
	}
	// if len(record.Subdivisions) > 0 {
	// 	fmt.Printf("subdivision name: %v\n", record.Subdivisions[0].Names["en"])
	// }
	// fmt.Printf("ISO country code: %v\n", record.Country.IsoCode)
	// fmt.Printf("Time zone: %v\n", record.Location.TimeZone)
	// fmt.Printf("Coordinates: %v, %v\n", record.Location.Latitude, record.Location.Longitude)

	var country string
	var city string
	cityRecord, _ := dbCity.City(ip)
	if cityRecord != nil {
		country = cityRecord.Country.Names["en"]
		city = cityRecord.City.Names["en"]
	}

	var asn string
	asnRecord, _ := dbASN.ASN(ip)
	if asnRecord != nil {
		asn = strconv.FormatUint(uint64(asnRecord.AutonomousSystemNumber), 10)
	}

	return &Peer{
		Ip:      ip,
		Country: country,
		City:    city,
		Asn:     asn,
	}, nil
}

func main() {
	flag.Parse()

	// open geo databases
	dbCity, err := geoip2.Open("GeoLite2-City.mmdb")
	if err != nil {
		log.Fatal(err)
	}
	defer dbCity.Close()

	dbASN, err := geoip2.Open("GeoLite2-ASN.mmdb")
	if err != nil {
		log.Fatal(err)
	}
	defer dbASN.Close()

	// start prometheus on /metrics
	go func() {
		log.Printf("Starting Prometheus web server, available at: http://%s/metrics\n", *addr)
		http.Handle("/metrics", promhttp.Handler())
		http.ListenAndServe(*addr, nil)
	}()

	// exec command: pmacctd
	cmd := exec.Command("pmacctd", "-r 1", "-c src_host,dst_host", "-P print", "-O json")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		log.Fatalf("cmd.Start() failed with '%s'\n", err)
	}

	// wait for either a term signal or a message indicating shutdown
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

	// handle stdout of pmacctd
	scanner := bufio.NewScanner(stdout)
	go func() {
		for scanner.Scan() {
			text := scanner.Text()
			if strings.HasPrefix(text, "{") {
				var f flow
				if err := json.Unmarshal([]byte(text), &f); err != nil {
					log.Fatal(err)
				}

				source, err := MakePeer(f.IpSrc, dbCity, dbASN)
				if err != nil && *verbose {
					log.Print(err)
				}
				destination, err := MakePeer(f.IpDst, dbCity, dbASN)
				if err != nil && *verbose {
					log.Print(err)
				}

				if *verbose {
					fmt.Printf("%+v\n%+v\n%+v\n\n", f, source, destination)
				}

				flowBytesTotal.With(
					prometheus.Labels{
						"ip_src":      f.IpSrc,
						"ip_dst":      f.IpDst,
						"country_src": source.Country,
						"country_dst": destination.Country,
						"asn_src":     source.Asn,
						"asn_dst":     destination.Asn,
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

	// wait a reason to exit
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
