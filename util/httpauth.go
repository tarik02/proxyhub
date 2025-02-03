package util

import (
	"encoding/base64"
	"net/http"
	"strings"
)

const proxyAuthorizationHeader = "Proxy-Authorization"

func HTTPBasicAuth(username, password string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))
}

func HTTPBearerAuth(token string) string {
	return "Bearer " + token
}

func PullProxyAuth(req *http.Request) (bool, string, string) {
	authheader := strings.SplitN(req.Header.Get(proxyAuthorizationHeader), " ", 2)
	req.Header.Del(proxyAuthorizationHeader)

	if len(authheader) != 2 || strings.ToLower(authheader[0]) != "basic" {
		return false, "", ""
	}

	userpassraw, err := base64.StdEncoding.DecodeString(authheader[1])
	if err != nil {
		return false, "", ""
	}

	userpass := strings.SplitN(string(userpassraw), ":", 2)
	if len(userpass) != 2 {
		return false, "", ""
	}

	return true, userpass[0], userpass[1]
}
