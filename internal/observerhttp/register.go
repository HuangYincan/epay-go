package observerhttp

import (
	"github.com/example/epay-go/internal/handler/admin"
	"github.com/example/epay-go/internal/middleware"
	"github.com/example/epay-go/internal/observerstore"
	"github.com/example/epay-go/pkg/jwt"
	"github.com/example/epay-go/pkg/observer"
	"github.com/gin-gonic/gin"
)

// Router is installed only on the dedicated TLS listener. No request/body/query
// logger is installed, including for malformed requests and recovery responses.
func Router(service *observerstore.PairingService) *gin.Engine {
	r := gin.New()
	r.RedirectTrailingSlash = false
	r.RedirectFixedPath = false
	r.Use(func(c *gin.Context) {
		defer func() {
			if recover() != nil {
				c.Abort()
				failure(c.Writer, 503, "unavailable")
			}
		}()
		c.Next()
	})
	h := NewHandler(service)
	r.NoRoute(func(c *gin.Context) {
		if h.Guard(c.Writer, c.Request) {
			failure(c.Writer, 400, "invalid_route")
		}
	})
	r.POST("/api/observer/v1/pairing/:operation", gin.WrapH(h))
	r.POST(observer.Path, func(c *gin.Context) { h.Events(c.Writer, c.Request) })
	r.POST(observer.HealthPath, func(c *gin.Context) { h.Health(c.Writer, c.Request) })
	adminRoutes := r.Group("/api/admin")
	adminRoutes.Use(func(c *gin.Context) {
		if !h.Guard(c.Writer, c.Request) {
			c.Abort()
			return
		}
		c.Next()
	})
	adminRoutes.POST("/auth/login", admin.Login)
	authorized := adminRoutes.Group("/observer", middleware.JWTAuth(jwt.TokenTypeAdmin))
	for _, path := range []string{"/grants", "/grants/revoke", "/bindings/revoke"} {
		authorized.POST(path, func(c *gin.Context) { h.ServeAdmin(c.Writer, c.Request, middleware.GetUserID(c)) })
	}
	for _, path := range []string{"/events/query", "/confirmation"} {
		authorized.POST(path, func(c *gin.Context) { h.ServeOperations(c.Writer, c.Request, middleware.GetUserID(c)) })
	}
	return r
}
