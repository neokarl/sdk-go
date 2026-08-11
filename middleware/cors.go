package middleware

import (
	"github.com/labstack/echo/v4"
	echomw "github.com/labstack/echo/v4/middleware"
)

// CORS for the dev shell. Production should pin to the deployed shell origin
// and disable wildcards.
func CORS(origins []string) echo.MiddlewareFunc {
	if len(origins) == 0 {
		origins = []string{"*"}
	}
	return echomw.CORSWithConfig(echomw.CORSConfig{
		AllowOrigins: origins,
		AllowHeaders: []string{
			echo.HeaderAuthorization,
			echo.HeaderContentType,
			HeaderRequestID,
			HeaderTenantID,
		},
		ExposeHeaders:    []string{HeaderRequestID},
		AllowCredentials: true,
		MaxAge:           3600,
	})
}
