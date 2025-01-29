package api

type Proxy struct {
	ID                string `json:"id"`
	Ready             bool   `json:"ready"`
	Port              int    `json:"port"`
	Uptime            int64  `json:"uptime"`
	ActiveConnections int    `json:"active_connections"`
}
