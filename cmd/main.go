package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/betamos/rdv"
)

var (
	flagVerbose bool
	flagLAddr   string
)

func usage() {
	fmt.Fprintf(flag.CommandLine.Output(), "usage:\n\trdv [ flags ] serve\n\trdv [ flags ] <dial|accept> ADDR TOKEN:\n\n")
	flag.PrintDefaults()
}

func init() {
	flag.Usage = usage
	flag.StringVar(&flagLAddr, "l", ":8080", "listening addr for serve")
	flag.BoolVar(&flagVerbose, "v", false, "print verbose logs")
}

func main() {
	var err error
	flag.Parse()
	command := flag.Arg(0)
	switch command {
	case "s", "serve":
		err = server()
	case "d", "dial":
		err = client(true)
	case "a", "accept":
		err = client(false)
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		log.Fatalln("ERR:", err)
	}
}

func logf() func(string, ...interface{}) {
	if flagVerbose {
		return log.Printf
	}
	log.SetFlags(0)
	return nil
}

func server() error {
	config := rdv.DefaultServerConfig
	config.Logf = logf()
	server := rdv.NewServer(nil)
	http.Handle("/", server)
	go server.Serve()
	log.Printf("starting rdv server on %v\n", flagLAddr)
	return http.ListenAndServe(flagLAddr, nil)
}

func client(dialer bool) error {
	client := rdv.Config{
		Logf: logf(),
	}
	addr := flag.Arg(1)
	token := flag.Arg(2)
	fn := client.Accept
	if dialer {
		fn = client.Dial
	}
	tStart := time.Now()
	conn, _, err := fn(addr, token, nil)
	if err != nil {
		return err
	}
	meta := conn.Meta()
	connType := "p2p"
	if conn.IsRelay() {
		connType = "relay"
	}
	if meta.ObservedAddr == nil {
		log.Printf("NOTICE: missing observed address\n")
	}
	log.Printf("CONNECTED: %s %v, %s\n", connType, conn.RemoteAddr(), formatSince(tStart))

	var (
		sent, received int64
		tConnected     = time.Now()
	)
	go func() {
		sent, _ = io.Copy(conn, os.Stdin)
		conn.Close()
	}()
	received, _ = io.Copy(os.Stdout, conn)
	log.Printf("DONE: %v <-> %v, %v\n", formatBytes(received), formatBytes(sent), formatSince(tConnected))
	return nil
}

func formatSince(t time.Time) string {
	return time.Since(t).Round(time.Millisecond).String()
}

func formatBytes(b int64) string {
	const unit = 1000
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB",
		float64(b)/float64(div), "kMGTPE"[exp])
}
