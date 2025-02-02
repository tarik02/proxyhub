package api

type Proxy struct {
	ID      string `json:"id"`
	Port    int    `json:"port"`
	Started int64  `json:"started"`
}
