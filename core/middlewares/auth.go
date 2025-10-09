package middlewares

import (
	"errors"
	"github.com/crawlab-team/crawlab/core/constants"
	"github.com/crawlab-team/crawlab/core/models/models"
	"github.com/crawlab-team/crawlab/core/models/service"
	"github.com/crawlab-team/crawlab/core/user"
	"github.com/crawlab-team/crawlab/core/utils"
	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
)

func AuthorizationMiddleware() gin.HandlerFunc {
	userSvc, _ := user.GetUserService()
	return func(c *gin.Context) {
		// disable auth for test
		if utils.IsAuthDisabled() {
			u, err := service.NewModelService[models.User]().GetOne(bson.M{"username": constants.DefaultAdminUsername}, nil)
			if err != nil {
				utils.HandleErrorInternalServerError(c, err)
				return
			}
			c.Set(constants.UserContextKey, u)
			c.Next()
			return
		}

		// token string
		tokenStr := utils.GetAPITokenFromContext(c)

		// validate token
		u, err := userSvc.CheckToken(tokenStr)
		if err != nil {
			// validation failed, return error response
			utils.HandleErrorUnauthorized(c, errors.New("invalid token"))
			return
		}

		// set user in context
		c.Set(constants.UserContextKey, u)

		// validation success
		c.Next()
	}
}

func SyncAuthorizationMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if utils.IsAuthDisabled() {
			c.Next()
			return
		}

		authKey := c.GetHeader("Authorization")

		if authKey != utils.GetAuthKey() {
			utils.HandleErrorUnauthorized(c, errors.New("invalid auth key"))
			return
		}

		c.Next()
	}
}
