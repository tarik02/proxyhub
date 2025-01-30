package util

import (
	"net"
)

func ListenToChan(listener net.Listener, connChan chan<- net.Conn) error {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return err
		}

		connChan <- conn
	}
}
