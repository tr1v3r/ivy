package main

import (
	"encoding/json"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	swaggerFiles "github.com/swaggo/files"     // swagger embed files
	ginSwagger "github.com/swaggo/gin-swagger" // gin-swagger middleware
	"github.com/tr1v3r/pkg/log"

	"github.com/tr1v3r/ivy"
	_ "github.com/tr1v3r/ivy/docs"
	"github.com/tr1v3r/ivy/driver"
	"github.com/tr1v3r/ivy/web"
)

//	@title			R1v3r's ivy engine
//	@version		1.0
//	@description	This is an ivy engine server.
//	@termsOfService	http://localhost/terms/

//	@contact.name	R1v3r
//	@contact.url	http://localhost/support
//	@contact.email	churiver@outlook.com

//	@license.name	MIT License
//	@license.url	https://www.mit.edu/~amini/LICENSE.md

//	@host		localhost:8080
//	@BasePath	/api/v1

//	@securityDefinitions.basic	BasicAuth

//	@externalDocs.description	OpenAPI
//	@externalDocs.url			https://swagger.io/resources/open-api/

var timeout, _ = time.ParseDuration(os.Getenv("SHUTDOWN_TIMEOUT"))

func main() {
	web.InitForest(web.DefaultBuilder(load()...))

	// No periodic refresh: rules are loaded once at startup and the
	// directives never change afterwards, so a timed full Build only
	// re-ran every processor on every node — with the shipped default
	// config (a curl processor at "/") that meant one upstream HTTP call
	// every 5s with zero traffic, replaying side effects and discarding
	// caches. Rebuild on demand with web.RefreshForest() once a reload
	// trigger exists (e.g. the management API or SIGHUP).
	if timeout == 0 {
		timeout = 3 * time.Second
	}
	web.Serve(timeout, register(gin.Default()))
}

func register(r *gin.Engine) *gin.Engine {
	apiV1 := r.Group("api/v1")
	{
		web.RegisterAPI(apiV1)
	}

	r.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

	return r
}

var defaultFilename = "../../conf/rules.json"

// RuleDataItem is one entry of the rules file: the tree path the
// directive applies to and the serialized processors to run there.
type RuleDataItem struct {
	Path       string `json:"path"`
	Processors []struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	} `json:"Processors"`
}

func load() (directives []ivy.Directive) {
	var filename = os.Getenv("RULES_FILE")
	if filename == "" {
		filename = defaultFilename
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		log.Error("read file fail: %s", err)
		return nil
	}

	var items = []RuleDataItem{}
	if err = json.Unmarshal(data, &items); err != nil {
		log.Error("unmarshal data fail: %s", err)
		return nil
	}
	for _, line := range items {
		var ops []driver.Processor
		for _, opData := range line.Processors {
			var op driver.Processor
			switch opData.Type {
			case "json":
				op = new(driver.JSONProcessor)
			case "yaml":
				op = new(driver.YAMLProcessor)
			case "curl":
				op = new(driver.CURLProcessor)
			case "xml":
				op = new(driver.XMLProcessor)
			case "toml":
				op = new(driver.TOMLProcessor)
			case "template":
				op = new(driver.TemplateProcessor)
			}
			if op != nil {
				if err := op.Load(opData.Data); err != nil {
					log.Warn("load Process fail: %s\ndata: %s", err, opData.Data)
				}
			}
			ops = append(ops, op)
		}
		directives = append(directives, ivy.NewDirective(line.Path, ops...))
	}
	return directives
}
