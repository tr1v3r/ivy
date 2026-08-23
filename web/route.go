package web

import (
	"github.com/gin-gonic/gin"
)

// RegisterAPI wires the /api/v1 rule-query and management routes onto the
// given router group. Most management routes are Ping placeholders;
// GET /rule is the implemented query endpoint.
func RegisterAPI(r *gin.RouterGroup) {
	// api root path ping pong
	r.GET("ping", Ping)

	// return rules
	rule := r.Group("rule")
	{
		// ping pong
		rule.GET("ping", Ping)

		// return node rule
		rule.GET("", GetRule)
		// return root template
		rule.GET("template", Ping)
		// return info about node
		rule.GET("info", Ping)

		// modify rule
		m := rule.Group("mod")
		{
			// update node rule
			m.POST("", Ping)
			// create or delete rule node
			m.POST("node", Ping)
			// replace template
			m.POST("template", Ping)
			// check Processors on node
			m.POST("check", Ping)
		}
	}

	// list nodes
	list := r.Group("list")
	{
		// list all rule tree name
		list.GET("name", Ping)
		// list all nodes
		list.GET("node", Ping)
	}

	// configure about ivy engine
	config := r.Group("config")
	{
		// return config
		config.GET("", Ping)
	}
}
