package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
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

// ruleTTL caches rule-tree content for this duration when positive: e.g.
// RULES_TTL=5m re-runs the directives' processors (curl rule URLs fetch
// fresh upstream content) on the first access after each window. Zero or
// invalid (the default) keeps the standard build-once behavior.
var ruleTTL, _ = time.ParseDuration(os.Getenv("RULES_TTL"))

// rulesBuilder picks the tree mode for the loaded rules: cache-TTL when
// RULES_TTL is positive, standard build-once otherwise.
func rulesBuilder(rules []ivy.Directive) ivy.TreeBuilder {
	if ruleTTL > 0 {
		return web.DefaultCacheBuilder(ruleTTL, rules...)
	}
	return web.DefaultBuilder(rules...)
}

func main() {
	// Startup is fail-loud (#58/W5): a missing/unreadable/corrupt rules
	// file must abort the process instead of silently serving an empty
	// tree. The SIGHUP path below deliberately differs — a running
	// server keeps its current forest when a reload fails.
	directives, err := load()
	if err != nil {
		log.Fatalf("load rules fail: %v", err)
	}
	web.InitForest(rulesBuilder(directives))

	// SIGHUP reloads RULES_FILE and swaps the forest in place (the swap
	// itself is race-free: InitForest holds forestMu). This is the reload
	// trigger replacing the removed 5s full-rebuild ticker (audit W3):
	// content freshness belongs to RULES_TTL caching, config changes to
	// an explicit signal. A failed reload (e.g. the file was corrupted
	// after startup) logs and keeps the current forest serving (#58):
	// a config mistake must not take down a healthy process.
	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGHUP)
		for range ch {
			log.Info("SIGHUP received: reloading rules")
			directives, err := load()
			if err != nil {
				log.Errorf("SIGHUP reload fail, keeping current forest: %v", err)
				continue
			}
			web.InitForest(rulesBuilder(directives))
		}
	}()

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

// load reads the rules file and builds directives (#58/W5):
//
//   - file-level failures (missing/unreadable file, invalid JSON) return
//     an error; the caller decides fail-loud (startup) vs keep-old-state
//     (SIGHUP reload) — load never silently yields an empty tree;
//   - processor-level failures degrade per-op: a processor whose Load
//     fails (e.g. a value of the wrong type) is dropped with an error
//     naming the rule and op index, and so is an op of unknown type
//     (#60/W7 — a typo like "Json" used to become a nil slot that
//     StdRealizer silently skipped). A half-unmarshaled or unknown
//     processor must never enter a chain, where it would silently emit
//     invalid content or no-op;
//   - a directive whose processors were all dropped (Load failures or
//     unknown types) is dropped entirely (an explicitly empty
//     "Processors": [] stays as-is).
func load() ([]ivy.Directive, error) {
	var filename = os.Getenv("RULES_FILE")
	if filename == "" {
		filename = defaultFilename
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("read rules file %q: %w", filename, err)
	}

	var items = []RuleDataItem{}
	if err = json.Unmarshal(data, &items); err != nil {
		return nil, fmt.Errorf("unmarshal rules file %q: %w", filename, err)
	}
	var directives []ivy.Directive
	for _, line := range items {
		var ops []driver.Processor
		for i, opData := range line.Processors {
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
			default:
				// unknown type (#60/W7): a typo like "Json" used to append a
				// nil processor that StdRealizer silently skipped
				// (driver/common.go) — the rule became a no-op with no
				// warning. Drop the op with an error naming the rule and op
				// index instead.
				log.Errorf("rules %q op %d: unknown processor type %q, op dropped", line.Path, i, opData.Type)
				continue
			}
			if err := op.Load(opData.Data); err != nil {
				log.Errorf("rules %q op %d (type %q): Load fail, op dropped: %v",
					line.Path, i, opData.Type, err)
				continue
			}
			ops = append(ops, op)
		}
		if len(ops) == 0 && len(line.Processors) > 0 {
			log.Errorf("rules %q: no processor could be loaded (all failed to Load or had unknown type), directive dropped", line.Path)
			continue
		}
		directives = append(directives, ivy.NewDirective(line.Path, ops...))
	}
	return directives, nil
}
