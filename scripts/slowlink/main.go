// Command slowlink is a test tool: it forwards TCP connections and holds
// every piece of data back for a fixed time in each direction, to stand in
// for a distant or roundabout connection to the index.
//
//	go run ./scripts/slowlink -listen 127.0.0.1:16390 -to 127.0.0.1:16380 -delay 255ms
//
// With -delay 255ms a request and its answer take 510 ms longer, which is
// what a colleague measured through a VPN that crossed the world twice.
package main

import (
	"flag"
	"log"
	"net"
	"time"
)

type piece struct {
	due  time.Time
	data []byte
}

// forward copies src to dst, releasing each piece delay after it arrived.
// Pieces that arrive together leave together: the delay is not added up.
func forward(dst, src net.Conn, delay time.Duration) {
	queue := make(chan piece, 1024)
	go func() {
		defer close(queue)
		for {
			buf := make([]byte, 32*1024)
			n, err := src.Read(buf)
			if n > 0 {
				queue <- piece{due: time.Now().Add(delay), data: buf[:n]}
			}
			if err != nil {
				return
			}
		}
	}()
	for p := range queue {
		time.Sleep(time.Until(p.due))
		if _, err := dst.Write(p.data); err != nil {
			break
		}
	}
	dst.Close()
	src.Close()
}

func main() {
	listen := flag.String("listen", "127.0.0.1:16390", "address to accept connections on")
	to := flag.String("to", "127.0.0.1:16380", "address to forward them to")
	delay := flag.Duration("delay", 255*time.Millisecond, "time to hold data back, in each direction")
	flag.Parse()
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("slowlink: %s -> %s, %v each way", *listen, *to, *delay)
	for {
		c, err := ln.Accept()
		if err != nil {
			log.Fatal(err)
		}
		go func() {
			up, err := net.Dial("tcp", *to)
			if err != nil {
				c.Close()
				return
			}
			go forward(up, c, *delay)
			forward(c, up, *delay)
		}()
	}
}
