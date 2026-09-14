package web

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/tr1v3r/ivy/driver"
)

// Ping status check
//
//	@Summary		Check status
//	@Description	return pong
//	@Tags			status
//	@Accept			plain
//	@Produce		json
//	@Success		200	{object}	map[string]any
//	@Router			/ping [get]
func Ping(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"message": "pong"}) }

// GetRule get target node's rule
//
//	@Summary		Get rule
//	@Description	return node's rule
//	@Tags			rule
//	@Accept			plain
//	@Produce		json
//	@Success		200	{object}	map[string]any
//	@Router			/rule [get]
func GetRule(c *gin.Context) {
	name := c.Query("name")
	path := c.Query("path")

	rc := driver.RealizeContext{Context: c.Request.Context(), Params: make(map[string]string)}
	for key, values := range c.Request.URL.Query() {
		if len(values) > 0 && key != "name" && key != "path" {
			rc.Params[key] = values[0]
		}
	}

	tree := f.Get(name)
	if tree == nil {
		// unknown or missing tree name must not panic: f.Get returns a nil
		// Tree interface, and calling GetWithContext on it dereferences nil.
		c.JSON(http.StatusNotFound, gin.H{
			"msg": fmt.Sprintf("tree %q not found", name),
		})
		return
	}

	rule, err := tree.GetWithContext(&rc, path)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"msg": fmt.Sprintf("query %s on %s fail: %s", path, name, err),
		})
		return
	}
	c.JSON(http.StatusOK, rule)
}
