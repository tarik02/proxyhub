package proxyclient

import (
	"net/http"

	"github.com/gorilla/websocket"
)

type ClientOptions struct {
	Endpoint string
	Token    string
	HTTP     *http.Client
	WSDialer *websocket.Dialer
}

func NewClientOptions() ClientOptions {
	return ClientOptions{
		HTTP:     http.DefaultClient,
		WSDialer: websocket.DefaultDialer,
	}
}
