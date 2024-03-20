package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/betamos/rdv"
)

var (
	flagVerbose bool
	flagRelay   bool
	flagLAddr   string

	spaces = rdv.DefaultSpaces
)

func usage() {
	fmt.Fprintf(flag.CommandLine.Output(), "usage:\n\trdv [ flags ] serve\n\trdv [ flags ] <dial|accept> ADDR TOKEN:\n\n")
	flag.PrintDefaults()
}

func init() {
	flag.Usage = usage
	flag.StringVar(&flagLAddr, "l", ":8080", "listening addr for serve")
	flag.BoolVar(&flagVerbose, "v", false, "print verbose logs")
	flag.BoolVar(&flagRelay, "r", false, "client: force using the relay even if p2p is possible")
}

func main() {
	var err error
	flag.Parse()
	if flagVerbose {
		log.SetFlags(log.Lmicroseconds)
		slog.SetLogLoggerLevel(slog.LevelDebug)
	} else {
		log.SetFlags(log.Ltime)
	}
	if flagRelay {
		spaces = rdv.NoSpaces
	}
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
		slog.Error("invalid args", "err", err)
	}
}

func server() error {
	server := rdv.NewServer(&rdv.ServerConfig{
		ServeFunc: handler,
	})
	http.Handle("/", server)
	go server.Serve(context.Background())
	slog.Info("listening", "addr", flagLAddr)
	return http.ListenAndServe(flagLAddr, nil)
}

func handler(ctx context.Context, dc, ac *rdv.Conn) {
	token := dc.Meta().Token
	slog.Info("matched", "token", token, "dial_addr", dc.Meta().ObservedAddr, "accept_addr", ac.Meta().ObservedAddr)

	r := new(rdv.Relayer)
	dn, an, err := r.Run(ctx, dc, ac)
	slog.Info("finished", "token", dc.Meta().Token, "dial_bytes", dn, "accept_bytes", an, "err", err)
}

func client(dialer bool) error {
	client := rdv.NewClient(&rdv.ClientConfig{
		AddrSpaces: spaces,
	})
	addr := flag.Arg(1)
	token := flag.Arg(2)
	fn := client.Accept
	if dialer {
		fn = client.Dial
	}
	tStart := time.Now()
	conn, _, err := fn(context.Background(), addr, token, nil)
	if err != nil {
		return err
	}
	meta := conn.Meta()
	if meta.ObservedAddr == nil {
		slog.Warn("client: missing observed address")
	}

	var (
		tx, rx     int64
		tConnected = time.Now()
		done       = make(chan struct{})
	)
	slog.Info("client: peer connected", "is_relay", conn.IsRelay(), "addr", conn.RemoteAddr(), "dur", tConnected.Sub(tStart))
	pr, pw := io.Pipe()
	go func() {
		io.Copy(pw, os.Stdin) // May never terminate
		pw.Close()
	}()
	go func() {
		tx, _ = io.Copy(conn, pr)
		conn.Close()
		close(done)
	}()
	rx, _ = io.Copy(os.Stdout, conn)
	pr.Close()
	<-done
	slog.Info("client: peer disconnected", "tx", tx, "rx", rx, "dur", time.Since(tConnected))
	return nil
}
