package main

import (
	"errors"
	"io"

	"github.com/gorilla/websocket"
)

type WSPipe struct {
	conn *websocket.Conn

	readConnToWs, readWsToConn   io.ReadCloser
	writeConnToWs, writeWsToConn io.WriteCloser
}

func NewWSPipe(conn *websocket.Conn) *WSPipe {
	readConnToWs, writeConnToWs := io.Pipe()
	readWsToConn, writeWsToConn := io.Pipe()

	go func() {
		for {
			t, r, err := conn.NextReader()
			if err != nil {
				readConnToWs.CloseWithError(err)
				writeWsToConn.CloseWithError(err)
				return
			}
			if t != websocket.BinaryMessage {
				continue
			}
			if _, err := io.Copy(writeWsToConn, r); err != nil {
				readConnToWs.CloseWithError(err)
				writeWsToConn.CloseWithError(err)
				return
			}
		}
	}()

	go func() {
		for {
			b := make([]byte, 4096)
			n, err := readConnToWs.Read(b)
			if err != nil {
				readConnToWs.CloseWithError(err)
				writeWsToConn.CloseWithError(err)
				_ = conn.Close()
				return
			}
			if err := conn.WriteMessage(websocket.BinaryMessage, b[:n]); err != nil {
				readConnToWs.CloseWithError(err)
				writeWsToConn.CloseWithError(err)
				return
			}
		}
	}()

	return &WSPipe{
		conn:          conn,
		readConnToWs:  readConnToWs,
		readWsToConn:  readWsToConn,
		writeConnToWs: writeConnToWs,
		writeWsToConn: writeWsToConn,
	}
}

func (p *WSPipe) Read(b []byte) (n int, err error) {
	return p.readWsToConn.Read(b)
}

func (p *WSPipe) Write(b []byte) (n int, err error) {
	return p.writeConnToWs.Write(b)
}

func (p *WSPipe) Close() error {
	return errors.Join(
		p.readConnToWs.Close(),
		p.writeConnToWs.Close(),
		p.conn.Close(),
	)
}

var _ io.ReadWriteCloser = (*WSPipe)(nil)
