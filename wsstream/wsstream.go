package wsstream

import (
	"errors"
	"io"
	"sync"

	"github.com/gorilla/websocket"
)

type WSStream struct {
	conn *websocket.Conn

	readIn, readOut   io.ReadCloser
	writeIn, writeOut io.WriteCloser

	writeTextChan       chan string
	writeTextClosed     chan struct{}
	writeTextClosedOnce sync.Once

	HandleTextMessage func(io.Reader)
}

func New(conn *websocket.Conn) *WSStream {
	readIn, writeIn := io.Pipe()
	readOut, writeOut := io.Pipe()

	writeBinaryChan := make(chan []byte)
	writeTextChan := make(chan string)
	writeTextClosed := make(chan struct{})

	res := &WSStream{
		conn:            conn,
		readIn:          readIn,
		readOut:         readOut,
		writeIn:         writeIn,
		writeOut:        writeOut,
		writeTextChan:   writeTextChan,
		writeTextClosed: writeTextClosed,
	}

	go func() {
		for {
			t, r, err := conn.NextReader()
			if err != nil {
				_ = writeIn.CloseWithError(err)
				return
			}

			switch t {
			case websocket.TextMessage:
				if res.HandleTextMessage != nil {
					res.HandleTextMessage(r)
				}

			case websocket.BinaryMessage:
				if _, err := io.Copy(writeIn, r); err != nil {
					_ = writeIn.CloseWithError(err)
					return
				}
			}
		}
	}()

	go func() {
		for {
			b := make([]byte, 4096)
			n, err := readOut.Read(b)
			if err != nil {
				_ = readOut.CloseWithError(err)
				return
			}
			writeBinaryChan <- b[:n]
		}
	}()

	go func() {
		// defer close(writeTextClosed)

		for {
			if writeBinaryChan == nil && writeTextChan == nil {
				return
			}

			select {
			case b, ok := <-writeBinaryChan:
				if !ok {
					writeBinaryChan = nil
					continue
				}
				if err := conn.WriteMessage(websocket.BinaryMessage, b); err != nil {
					_ = readOut.CloseWithError(err)
					return
				}

			case s := <-writeTextChan:
				// if !ok {
				// 	writeTextChan = nil
				// 	continue
				// }
				if err := conn.WriteMessage(websocket.TextMessage, []byte(s)); err != nil {
					_ = readOut.CloseWithError(err)
					return
				}

			case <-writeTextClosed:
				writeTextChan = nil
				writeTextClosed = nil
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

func (p *WSStream) WriteText(s string) (err error) {
	select {
	case <-p.writeTextClosed:
		return io.ErrClosedPipe
	case p.writeTextChan <- s:
		return nil
	}
}

func (p *WSStream) Close() error {
	p.writeTextClosedOnce.Do(func() {
		close(p.writeTextClosed)
	})
	return errors.Join(
		p.readIn.Close(),
		p.writeOut.Close(),
		p.conn.Close(),
	)
}

var _ io.ReadWriteCloser = (*WSStream)(nil)
