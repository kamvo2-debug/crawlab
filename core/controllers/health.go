package controllers

import (
	"github.com/gin-gonic/gin"
	"net/http"
)

func GetHealthFn(healthFn func() bool) func(c *gin.Context) {
	return func(c *gin.Context) {
		if healthFn() {
			_, _ = c.Writer.Write([]byte("ok"))
			c.AbortWithStatus(http.StatusOK)
		}
		_, _ = c.Writer.Write([]byte("not ready"))
		c.AbortWithStatus(http.StatusServiceUnavailable)
	}
}
