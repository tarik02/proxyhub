package api

type Proxy struct {
	ID              string   `json:"id"`
	Version         string   `json:"version"`
	Port            int      `json:"port"`
	Started         int64    `json:"started"`
	EgressWhitelist []string `json:"egress_whitelist"`
}
