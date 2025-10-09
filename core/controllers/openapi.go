package controllers

import (
	"github.com/crawlab-team/fizz/openapi"
	"github.com/gin-gonic/gin"
)

func GetOpenAPI(c *gin.Context) {
	f := globalWrapper.GetFizz()

	info := &openapi.Info{
		Title:       "Crawlab API",
		Description: "REST API for Crawlab",
		Version:     "0.7.0",
	}

	handleFunc := f.OpenAPI(info, "json")
	handleFunc(c)
}
