package logging

import (
	"reflect"

	"github.com/go-viper/mapstructure/v2"
	"go.uber.org/zap/zapcore"
)

func StringToLogLevelHookFunc() mapstructure.DecodeHookFunc {
	return func(f reflect.Type, t reflect.Type, data any) (any, error) {
		if t != reflect.TypeFor[zapcore.Level]() {
			return data, nil
		}
		if d, ok := data.(string); ok {
			return zapcore.ParseLevel(d)
		}
		return data, nil
	}
}
