package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/lmittmann/tint"

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
	var level = slog.LevelInfo
	if flagVerbose {
		level = slog.LevelDebug
	}
	handler := tint.NewHandler(os.Stderr, &tint.Options{
		Level:      level,
		TimeFormat: time.TimeOnly,
	})
	slog.SetDefault(slog.New(handler))
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
	server := rdv.NewServer(nil)
	http.Handle("/", server)
	go server.Serve(context.Background())
	slog.Info("server: listening", "addr", flagLAddr)
	return http.ListenAndServe(flagLAddr, nil)
}

func client(dialer bool) error {
	client := rdv.NewClient(&rdv.ClientConfig{
		AddrSpaces: rdv.PublicSpaces,
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
