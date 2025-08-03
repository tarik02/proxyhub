package wsstream

import (
	"errors"
	"io"

	"github.com/gorilla/websocket"
)

type WSStream struct {
	conn *websocket.Conn

	readIn, readOut   io.ReadCloser
	writeIn, writeOut io.WriteCloser
}

func New(conn *websocket.Conn) *WSStream {
	readIn, writeIn := io.Pipe()
	readOut, writeOut := io.Pipe()

	res := &WSStream{
		conn:     conn,
		readIn:   readIn,
		readOut:  readOut,
		writeIn:  writeIn,
		writeOut: writeOut,
	}

	go func() {
		for {
			t, r, err := conn.NextReader()
			if err != nil {
				_ = writeIn.CloseWithError(err)
				return
			}

			switch t {
			case websocket.BinaryMessage:
				if _, err := io.Copy(writeIn, r); err != nil {
					_ = writeIn.CloseWithError(err)
					return
				}
			}
		}
	}()

	go func() {
		b := make([]byte, 16*1024)
		for {
			n, err := readOut.Read(b)
			if err != nil {
				_ = readOut.CloseWithError(err)
				return
			}
			if err := conn.WriteMessage(websocket.BinaryMessage, b[:n]); err != nil {
				_ = readOut.CloseWithError(err)
				return
			}
		}
	}()

	return res
}

func (p *WSStream) Read(b []byte) (n int, err error) {
	return p.readIn.Read(b)
}

func (p *WSStream) Write(b []byte) (n int, err error) {
	return p.writeOut.Write(b)
}

func (p *WSStream) Close() error {
	return errors.Join(
		p.readIn.Close(),
		p.writeOut.Close(),
		p.conn.Close(),
	)
}

var _ io.ReadWriteCloser = (*WSStream)(nil)
