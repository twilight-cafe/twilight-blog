package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"

	"github.com/labstack/echo/v4"
)

func main() {
	e := echo.New()
	e.HideBanner = true

	admin := e.Group("/api/admin", func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			fmt.Println("  ADMIN-GROUP-MW ran; c.Request().URL.Path =", c.Request().URL.Path)
			return echo.NewHTTPError(http.StatusUnauthorized, "unauthorized")
		}
	})
	admin.GET("/users", func(c echo.Context) error { return c.String(200, "USERS") })
	e.GET("/*", func(c echo.Context) error {
		fmt.Println("  WILDCARD ran; c.Request().URL.Path =", c.Request().URL.Path)
		if len(c.Request().URL.Path) >= 4 && c.Request().URL.Path[:4] == "/api" {
			return echo.ErrNotFound
		}
		return c.String(200, "SPA")
	})

	for _, p := range []string{"/api/admin/users", "/api/admin/../admin/users", "//api/admin/users"} {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		fmt.Printf("input=%q -> httptest URL.Path=%q\n", p, req.URL.Path)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		fmt.Printf("  -> %d body=%s\n", rec.Code, rec.Body.String())
	}
}
