package ivy_test

import (
	"fmt"

	"github.com/tr1v3r/ivy"
	"github.com/tr1v3r/ivy/driver"
)

func ExampleTree_Graft() {
	root, err := ivy.NewJSONTree[ivy.Directive]("cfg", `{"enabled":true}`)
	if err != nil {
		panic(err)
	}
	branch, err := ivy.NewLazyJSONTree("source-identity", `{}`,
		ivy.NewDirective("/", &driver.JSONProcessor{
			T: "create", JSONPath: "region", V: []byte("beijing"),
		}),
		ivy.NewDirective("/users"),
	)
	if err != nil {
		panic(err)
	}
	if err := root.Graft("/api", branch); err != nil {
		panic(err)
	}
	live, err := ivy.NewLazyInstantJSONTree("live", `{}`,
		ivy.NewDirective("/", &driver.JSONProcessor{
			T: "set", JSONPath: "live", V: []byte("true"),
		}),
	)
	if err != nil {
		panic(err)
	}
	// Continue operating from root, even inside a previously mounted source.
	if err := root.Graft("/api/users/live", live); err != nil {
		panic(err)
	}
	value, err := root.Get("/api/users/live")
	if err != nil {
		panic(err)
	}
	fmt.Println(string(value))
	fmt.Println(branch.Name(), branch.Path(), live.Path())
	// Output:
	// {"enabled":true,"region":"beijing","live":true}
	// source-identity /api /api/users/live
}
