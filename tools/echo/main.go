// TCP + UDP 回显服务器，用于本地转发链路测试。
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"time"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:2001", "监听地址")
	flag.Parse()

	// TCP echo
	tcpLn, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	go func() {
		for {
			c, err := tcpLn.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				buf := make([]byte, 65536)
				for {
					c.SetReadDeadline(time.Now().Add(30 * time.Second))
					n, err := c.Read(buf)
					if n > 0 {
						fmt.Fprintf(c, "ECHO(%d):", n)
						c.Write(buf[:n])
					}
					if err == io.EOF {
						return
					}
					if err != nil {
						return
					}
				}
			}()
		}
	}()

	// UDP echo
	udpAddr, _ := net.ResolveUDPAddr("udp", *addr)
	udpConn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		log.Fatal(err)
	}
	go func() {
		buf := make([]byte, 65536)
		for {
			n, raddr, err := udpConn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			reply := append([]byte("ECHO:"), buf[:n]...)
			udpConn.WriteToUDP(reply, raddr)
		}
	}()

	log.Printf("echo server listening on %s (tcp+udp)", *addr)
	select {}
}
