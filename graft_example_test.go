package ivy_test

import (
	"fmt"

	"github.com/tr1v3r/ivy"
	"github.com/tr1v3r/ivy/driver"
)

func ExampleGraftAt() {
	root, err := ivy.NewJSONTree[ivy.Directive]("cfg", `{"enabled":true}`)
	if err != nil {
		panic(err)
	}
	branch, err := ivy.NewLazyJSONTree("branch", `{}`,
		ivy.NewDirective("/", &driver.JSONProcessor{
			T: "create", JSONPath: "region", V: []byte("beijing"),
		}),
	)
	if err != nil {
		panic(err)
	}
	if err := ivy.GraftAt(root, "/api", branch); err != nil {
		panic(err)
	}
	value, err := root.Get("/api")
	if err != nil {
		panic(err)
	}
	fmt.Println(string(value))
	// Output: {"enabled":true,"region":"beijing"}
}
