// Listen holds a TCP port until killed. Used by pane tests.
package main

import (
	"net"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		os.Exit(2)
	}
	ln, err := net.Listen("tcp", os.Args[1])
	if err != nil {
		os.Exit(1)
	}
	defer ln.Close()
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		_ = c.Close()
	}
}
