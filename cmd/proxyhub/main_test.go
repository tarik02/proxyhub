package main

import (
	"os"
	"testing"

	"go.uber.org/goleak"
)

func TestLeaks(t *testing.T) {
	defer goleak.VerifyNone(
		t,
		goleak.IgnoreAnyFunction("github.com/spf13/viper.(*Viper).WatchConfig.func1"),
		goleak.IgnoreTopFunction("github.com/spf13/viper.(*Viper).WatchConfig.func1.1"),
		goleak.IgnoreAnyFunction("github.com/fsnotify/fsnotify.(*inotify).readEvents"),
	)
	_ = os.Chdir("../..")

	main()
}
