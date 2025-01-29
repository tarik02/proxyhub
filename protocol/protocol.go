package protocol

import (
	"encoding/binary"
	"fmt"
	"io"
)

type CmdID byte

const (
	CmdIDNew     CmdID = 0
	CmdIDClose   CmdID = 1
	CmdIDData    CmdID = 2
	CmdIDMessage CmdID = 3
)

const MaxDataSize = 32 * 1024 * 1024

var ErrInvalidCmdID = fmt.Errorf("invalid command ID")

type Cmd any

type CmdNew struct {
	ID uint32
}

type CmdClose struct {
	ID uint32
}

type CmdData struct {
	ID    uint32
	Bytes []byte
}

type CmdMessage struct {
	Message string
}

func ReadCmd(r io.Reader) (Cmd, error) {
	var buf [16]byte
	if _, err := io.ReadFull(r, buf[0:1]); err != nil {
		return nil, err
	}

	switch CmdID(buf[0]) {
	case CmdIDNew:
		var cmd CmdNew
		if _, err := io.ReadFull(r, buf[0:4]); err != nil {
			return nil, err
		}
		cmd.ID = binary.BigEndian.Uint32(buf[0:4])
		return cmd, nil

	case CmdIDClose:
		var cmd CmdClose
		if _, err := io.ReadFull(r, buf[0:4]); err != nil {
			return nil, err
		}
		cmd.ID = binary.BigEndian.Uint32(buf[0:4])
		return cmd, nil

	case CmdIDData:
		var cmd CmdData
		if _, err := io.ReadFull(r, buf[0:8]); err != nil {
			return nil, err
		}
		cmd.ID = binary.BigEndian.Uint32(buf[0:4])
		l := binary.BigEndian.Uint32(buf[4:8])
		if l > MaxDataSize {
			return nil, fmt.Errorf("data too long: %d", l)
		}
		cmd.Bytes = make([]byte, l)
		if _, err := io.ReadFull(r, cmd.Bytes); err != nil {
			return nil, err
		}
		return cmd, nil

	case CmdIDMessage:
		var cmd CmdMessage
		if _, err := io.ReadFull(r, buf[0:4]); err != nil {
			return nil, err
		}
		l := binary.BigEndian.Uint32(buf[0:4])
		if l > MaxDataSize {
			return nil, fmt.Errorf("message too long: %d", l)
		}
		msg := make([]byte, l)
		if _, err := io.ReadFull(r, msg); err != nil {
			return nil, err
		}
		cmd.Message = string(msg)
		return cmd, nil

	default:
		return nil, ErrInvalidCmdID
	}
}

func WriteCmd(w io.Writer, cmd Cmd) error {
	var buf [16]byte
	switch cmd := cmd.(type) {
	case CmdNew:
		buf[0] = byte(CmdIDNew)
		binary.BigEndian.PutUint32(buf[1:5], cmd.ID)
		if _, err := w.Write(buf[0:5]); err != nil {
			return err
		}
		return nil

	case CmdClose:
		buf[0] = byte(CmdIDClose)
		binary.BigEndian.PutUint32(buf[1:5], cmd.ID)
		if _, err := w.Write(buf[0:5]); err != nil {
			return err
		}
		return nil

	case CmdData:
		buf[0] = byte(CmdIDData)
		binary.BigEndian.PutUint32(buf[1:5], cmd.ID)
		binary.BigEndian.PutUint32(buf[5:9], uint32(len(cmd.Bytes))) // nolint:gosec
		if _, err := w.Write(buf[0:9]); err != nil {
			return err
		}
		if _, err := w.Write(cmd.Bytes); err != nil {
			return err
		}
		return nil

	case CmdMessage:
		buf[0] = byte(CmdIDMessage)
		binary.BigEndian.PutUint32(buf[1:5], uint32(len(cmd.Message))) // nolint:gosec
		if _, err := w.Write(buf[0:5]); err != nil {
			return err
		}
		if _, err := w.Write([]byte(cmd.Message)); err != nil {
			return err
		}
		return nil

	default:
		return ErrInvalidCmdID
	}
}
