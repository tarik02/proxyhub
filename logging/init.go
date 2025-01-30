package logging

import (
	"strings"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func init() {
	gin.DebugPrintFunc = func(format string, values ...any) {
		zap.S().Debugf(strings.TrimSuffix(format, "\n"), values...)
	}
}
