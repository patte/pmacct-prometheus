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
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"inet.af/netaddr"

	"github.com/oschwald/geoip2-golang"

	"tailscale.com/net/interfaces"
)

var (
	addr    = flag.String("addr", ":9590", "Listening Address for /metrics")
	verbose = flag.Bool("verbose", false, "Be chatty on stdout")
)

// {"event_type": "purge", "ip_src": "10.0.1.1", "ip_dst": "10.0.2.1", "packets": 2, "bytes": 143}
type Flow struct {
	IpSrcRaw    string `json:"ip_src"`
	IpDstRaw    string `json:"ip_dst"`
	IpSrc       netaddr.IP
	IpDst       netaddr.IP
	Packages    int    `json:"packets"`
	Bytes       int    `json:"bytes"`
	Proto       string `json:"proto"`
	Direction   string
	Private     bool
	PrivateRaw  string
	Source      *Peer
	Destination *Peer
}
type Peer struct {
	Ip         netaddr.IP
	Country    string
	CountryISO string
	City       string
	Asn        string
	AsnOrg     string
	Latitude   float64
	Longitude  float64
}

func MakeFlow(text string, localIps []netaddr.IP, dbCity *geoip2.Reader, dbASN *geoip2.Reader) (*Flow, error) {
	f := Flow{}
	if err := json.Unmarshal([]byte(text), &f); err != nil {
		return nil, err
	}

	source, err := MakePeer(f.IpSrcRaw, dbCity, dbASN)
	if err != nil && *verbose {
		return nil, err
	}
	destination, err := MakePeer(f.IpDstRaw, dbCity, dbASN)
	if err != nil && *verbose {
		return nil, err
	}

	f.IpSrc = source.Ip
	f.IpDst = destination.Ip

	f.Source = source
	f.Destination = destination

	f.Direction = GetDirection(f, localIps)
	f.Private = source.Ip.IsPrivate() && destination.Ip.IsPrivate()
	if f.Private {
		f.PrivateRaw = "private"
	} else {
		f.PrivateRaw = "public"
	}

	return &f, nil
}

func MakePeer(ipRaw string, dbCity *geoip2.Reader, dbASN *geoip2.Reader) (*Peer, error) {
	ip, err := netaddr.ParseIP(ipRaw)
	if err != nil {
		return nil, err
	}

	var country string
	var countryISO string
	var city string
	var latitude float64
	var longitude float64
	cityRecord, _ := dbCity.City(ip.IPAddr().IP)
	if cityRecord != nil {
		country = cityRecord.Country.Names["en"]
		countryISO = cityRecord.Country.IsoCode
		city = cityRecord.City.Names["en"]
		latitude = cityRecord.Location.Latitude
		longitude = cityRecord.Location.Longitude
	}

	var asn string
	var asnOrg string
	asnRecord, _ := dbASN.ASN(ip.IPAddr().IP)
	if asnRecord != nil {
		asn = strconv.FormatUint(uint64(asnRecord.AutonomousSystemNumber), 10)
		asnOrg = asnRecord.AutonomousSystemOrganization
	}

	return &Peer{
		Ip:         ip,
		Country:    country,
		CountryISO: countryISO,
		City:       city,
		Asn:        asn,
		AsnOrg:     asnOrg,
		Latitude:   latitude,
		Longitude:  longitude,
	}, nil
}

func containsIP(ips []netaddr.IP, ip netaddr.IP) bool {
	for _, ip1 := range ips {
		if ip1 == ip {
			return true
		}
	}
	return false
}

func GetDirection(f Flow, localIps []netaddr.IP) string {
	if containsIP(localIps, f.IpDst) {
		return "in"
	}
	if containsIP(localIps, f.IpSrc) {
		return "out"
	}
	return "unknown"
}

var (
	flowDirectionBytes = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "flow_direction_bytes",
			Help: "in or out Bytes",
		},
		[]string{"direction", "private", "country", "asn", "asn_org"},
	)
	// flowBytesTotal = promauto.NewCounterVec(
	// 	prometheus.CounterOpts{
	// 		Name: "flow_bytes_total",
	// 		Help: "Flow Bytes.",
	// 	},
	// 	[]string{"ip_src", "ip_dst", "country_src", "country_dst", "asn_src", "asn_dst", "direction"},
	// )
)

func LogPrometheus(flow *Flow) {
	if flow.Direction == "in" || flow.Direction == "out" {
		var peer *Peer
		if flow.Direction == "in" {
			peer = flow.Source
		} else {
			peer = flow.Destination
		}
		flowDirectionBytes.With(
			prometheus.Labels{
				"direction": flow.Direction,
				"private":   flow.PrivateRaw,
				// "ip":        peer.Ip.String(),
				"country": peer.Country,
				"asn":     peer.Asn,
				"asn_org": peer.AsnOrg,
			},
		).Add(float64(flow.Bytes))
	}
	// flowBytesTotal.With(
	// 	prometheus.Labels{
	// 		"ip_src":      flow.IpSrc.String(),
	// 		"ip_dst":      flow.IpDst.String(),
	// 		"country_src": flow.Source.Country,
	// 		"country_dst": flow.Destination.Country,
	// 		"asn_src":     flow.Source.Asn,
	// 		"asn_dst":     flow.Destination.Asn,
	// 		"direction":   flow.Direction,
	// 	},
	// ).Add(float64(flow.Bytes))
}

func main() {
	flag.Parse()

	// get local ip addresses
	localIps, _, err := interfaces.LocalAddresses()
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Local ips: %s\n", localIps)

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

	// exec command: pmacctd
	// https://github.com/pmacct/pmacct/blob/master/QUICKSTART
	// https://github.com/pmacct/pmacct/blob/6579ebeccdd0dd33e013a20a0b12a89c1bd65e94/sql/pmacct-create-table_v9.pgsql
	//
	cmd := exec.Command("pmacctd", "-r 1", "-c src_host,dst_host,src_port,dst_port,proto", "-P print", "-O json")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		log.Fatalf("cmd.Start() failed with '%s'\n", err)
	}
	// handle stdout of pmacctd
	scanner := bufio.NewScanner(stdout)
	go func() {
		for scanner.Scan() {
			text := scanner.Text()
			if strings.HasPrefix(text, "{") {

				flow, err := MakeFlow(text, localIps, dbCity, dbASN)
				if err != nil {
					log.Fatal(err)
				}

				if *verbose {
					// fmt.Printf("%s\n", text)
					fmt.Printf("%+v\n%+v\n%+v\n\n", flow, flow.Source, flow.Destination)
				}

				LogPrometheus(flow)
			} else {
				fmt.Println(text)
				// TODO identify exit message by pmacct
				// wg.Done()
			}
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
