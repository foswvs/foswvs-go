package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/foswvs/foswvs-go/internal/auth"
	"github.com/foswvs/foswvs-go/internal/bandwidth"
	"github.com/foswvs/foswvs-go/internal/db"
	"github.com/foswvs/foswvs-go/internal/gpio"
	"github.com/foswvs/foswvs-go/internal/handlers"
	"github.com/foswvs/foswvs-go/internal/iptables"
	"github.com/foswvs/foswvs-go/internal/network"
	"github.com/foswvs/foswvs-go/internal/ws"
)

var Version = "dev"

// Ensure dev stubs satisfy the interfaces at compile time.
var _ handlers.Firewall = (*iptables.DevIPT)(nil)
var _ handlers.CoinAcceptor = (*gpio.DevCoinslot)(nil)
var _ handlers.Firewall = (*iptables.IPT)(nil)
var _ handlers.CoinAcceptor = (*gpio.Coinslot)(nil)

func main() {
	var (
		addr    = flag.String("addr", ":80", "HTTP listen address")
		tlsAddr = flag.String("tls-addr", ":443", "HTTPS listen address")
		dataDir = flag.String("data-dir", "/home/pi/foswvs-go", "Data directory for DB, certs, config")
		webDir  = flag.String("web-dir", "", "Static web files directory (empty = embedded)")
		certF   = flag.String("tls-cert", "", "TLS certificate file")
		keyF    = flag.String("tls-key", "", "TLS key file")
		iface   = flag.String("iface", "wlan0", "Wireless interface name")
		dspeed  = flag.Int("dspeed", 0, "Download speed limit in Kbps (0=unlimited)")
		uspeed  = flag.Int("uspeed", 0, "Upload speed limit in Kbps (0=unlimited)")
		version = flag.Bool("version", false, "Print version and exit")
	)
	flag.Parse()

	if *version {
		fmt.Println(Version)
		os.Exit(0)
	}

	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)

	devMode := os.Getenv("FOSWVS_DEV") == "1"
	if devMode {
		log.Println("foswvs-go starting in DEV mode (stubs for GPIO + iptables)")
	} else {
		log.Println("foswvs-go starting...")
	}

	// --- Database ---
	store, err := db.Open(*dataDir)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer store.Close()

	// --- Core services (real vs dev) ---
	var ipt handlers.Firewall
	var coinslot handlers.CoinAcceptor

	gpioCfg := gpio.LoadConfig(*dataDir)
	if devMode {
		ipt = iptables.NewDev()
		coinslot = gpio.NewDevCoinslot(gpioCfg.SlotPin, gpioCfg.SensorPin)
	} else {
		ipt = iptables.New()
		coinslot = gpio.NewCoinslot(gpioCfg.SlotPin, gpioCfg.SensorPin)
	}

	net := network.New(*iface)
	sessions := auth.NewSessionStore()
	hub := ws.NewHub()

	// --- Device token secret (encrypts the localStorage token browsers use
	// to reclaim their balance after their MAC address rotates) ---
	deviceTokenSecret, err := auth.ReadOrCreateDeviceTokenSecret(*dataDir)
	if err != nil {
		log.Fatalf("device token secret: %v", err)
	}

	// --- Bandwidth shaper (skip in dev mode) ---
	if !devMode && (*dspeed > 0 || *uspeed > 0) {
		bw := bandwidth.New(*iface)
		if err := bw.Apply(*dspeed, *uspeed); err != nil {
			log.Printf("bandwidth shaper: %v", err)
		}
		defer bw.Clear()
	}

	// --- Build handler ---
	app := &handlers.App{
		Store:       store,
		IPT:         ipt,
		Net:         net,
		Coinslot:    coinslot,
		Auth:        sessions,
		Hub:         hub,
		DataDir:     *dataDir,
		WebDir:      *webDir,
		DevMode:     devMode,
		JWTSecret:   deviceTokenSecret,
		Maintenance: handlers.NewMaintenanceState(*dataDir),
	}

	mux := app.Routes()

	// --- Start background goroutines ---
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 1. WebSocket hub
	go hub.Run(ctx)

	// 2. DHCP lease watcher — polls ARP table, registers new devices
	go app.DHCPWatcher(ctx, net)

	// 3. Iptables byte counter poller — tracks data usage, kicks exhausted clients
	go app.UsagePoller(ctx)

	// 4. Share-tx cleaner
	go app.ShareTxCleaner(ctx)

	// --- HTTP server ---
	srv := &http.Server{
		Addr:         *addr,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		log.Printf("HTTP listening on %s", *addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http: %v", err)
		}
	}()

	// --- Optional TLS server ---
	var tlsSrv *http.Server
	if *certF != "" && *keyF != "" {
		tlsSrv = &http.Server{
			Addr:         *tlsAddr,
			Handler:      mux,
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 60 * time.Second,
			IdleTimeout:  120 * time.Second,
		}
		go func() {
			log.Printf("HTTPS listening on %s", *tlsAddr)
			if err := tlsSrv.ListenAndServeTLS(*certF, *keyF); err != nil && err != http.ErrServerClosed {
				log.Fatalf("https: %v", err)
			}
		}()
	}

	// --- Graceful shutdown ---
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	log.Println("shutting down...")
	cancel()

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutCancel()
	srv.Shutdown(shutCtx)
	if tlsSrv != nil {
		tlsSrv.Shutdown(shutCtx)
	}
}
