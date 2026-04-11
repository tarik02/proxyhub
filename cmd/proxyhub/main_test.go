package main

import (
	"net"
	"os"
	"testing"

	"go.uber.org/goleak"
)

func TestLeaks(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:8080")
	if err != nil {
		t.Skipf("skipping leak test because :8080 is unavailable: %v", err)
	}
	_ = ln.Close()

	defer goleak.VerifyNone(
		t,
		goleak.IgnoreAnyFunction("github.com/spf13/viper.(*Viper).WatchConfig.func1"),
		goleak.IgnoreTopFunction("github.com/spf13/viper.(*Viper).WatchConfig.func1.1"),
		goleak.IgnoreAnyFunction("github.com/fsnotify/fsnotify.(*inotify).readEvents"),
	)
	_ = os.Chdir("../..")

	main()
}
