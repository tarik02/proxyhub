package util

import (
	"reflect"

	"github.com/gobwas/glob"
	"github.com/mitchellh/mapstructure"
)

func StringToGlobHookFunc() mapstructure.DecodeHookFunc {
	return func(f reflect.Type, t reflect.Type, data any) (any, error) {
		if t != reflect.TypeFor[glob.Glob]() {
			return data, nil
		}
		if d, ok := data.(string); ok {
			return glob.Compile(d)
		}
		return data, nil
	}
}
