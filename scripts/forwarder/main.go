// Command forwarder is a test tool: a TCP forwarder whose behaviour can be
// changed while it runs, to rehearse a storage provider that refuses
// connections or stops answering.
//
//	forwarder -listen 127.0.0.1:19300 -to 127.0.0.1:19000 -mode-file /tmp/mode
//
// The file holds one word, read every 200 ms:
//
//	ok       pass everything through
//	refuse   close every connection at once, new and old
//	silent   accept connections and hold them, passing nothing
package main

import (
	"flag"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

var mode atomic.Value // string

func current() string { return mode.Load().(string) }

func main() {
	listen := flag.String("listen", "", "address to listen on")
	to := flag.String("to", "", "address to forward to")
	modeFile := flag.String("mode-file", "", "file holding ok, refuse or silent")
	flag.Parse()
	if *listen == "" || *to == "" || *modeFile == "" {
		flag.Usage()
		os.Exit(64)
	}
	mode.Store("ok")
	go func() {
		for {
			if b, err := os.ReadFile(*modeFile); err == nil {
				if m := strings.TrimSpace(string(b)); m == "ok" || m == "refuse" || m == "silent" {
					mode.Store(m)
				}
			}
			time.Sleep(200 * time.Millisecond)
		}
	}()
	l, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	for {
		c, err := l.Accept()
		if err != nil {
			log.Fatal(err)
		}
		go handle(c, *to)
	}
}

func handle(c net.Conn, to string) {
	defer c.Close()
	switch current() {
	case "refuse":
		return
	case "silent":
		for current() == "silent" {
			time.Sleep(200 * time.Millisecond)
		}
		return // the client has long given up by now; close
	}
	up, err := net.DialTimeout("tcp", to, 5*time.Second)
	if err != nil {
		return
	}
	defer up.Close()
	done := make(chan struct{}, 2)
	pipe := func(dst, src net.Conn) {
		buf := make([]byte, 32*1024)
		for {
			src.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
			n, err := src.Read(buf)
			if n > 0 {
				for current() == "silent" { // hold the data
					time.Sleep(200 * time.Millisecond)
				}
				if current() == "refuse" {
					break
				}
				if _, werr := dst.Write(buf[:n]); werr != nil {
					break
				}
			}
			if err != nil {
				if ne, ok := err.(net.Error); ok && ne.Timeout() {
					if current() == "refuse" {
						break
					}
					continue
				}
				if err != io.EOF {
					break
				}
				break
			}
		}
		done <- struct{}{}
	}
	go pipe(up, c)
	go pipe(c, up)
	<-done
}
