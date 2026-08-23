package ivy

import (
	"context"
	"fmt"
	"time"

	"github.com/tr1v3r/ivy/driver"
)

// NewForest builds a new forest and returns it.
func NewForest(builders ...TreeBuilder) Forest {
	return (&forest{
		m:        make(map[string]Tree, len(builders)),
		builders: builders,
		builderM: make(map[string]TreeBuilder, len(builders)),
	}).Build()
}

// NewJSONTree builds a JSON tree.
func NewJSONTree[R Directive](name, template string, directives ...R) (Tree, error) {
	return NewTree(driver.NewJSONDriver(), name, template, directives...)
}

// NewLazyJSONTree builds a lazy JSON tree.
func NewLazyJSONTree[R Directive](name, template string, directives ...R) (Tree, error) {
	return NewLazyTree(driver.NewJSONDriver(), name, template, directives...)
}

// NewLazyInstantJSONTree builds a lazy instant JSON tree.
func NewLazyInstantJSONTree[R Directive](name, template string, directives ...R) (Tree, error) {
	return NewLazyInstantTree(driver.NewJSONDriver(), name, template, directives...)
}

// NewLazyCacheJSONTree builds a lazy JSON tree with cache TTL.
func NewLazyCacheJSONTree[R Directive](name, template string, ttl time.Duration, directives ...R) (Tree, error) {
	return NewLazyCacheTree(driver.NewJSONDriver(), name, template, ttl, directives...)
}

// NewYAMLTree builds a YAML tree.
func NewYAMLTree[R Directive](name, template string, directives ...R) (Tree, error) {
	return NewTree(driver.NewYAMLDriver(), name, template, directives...)
}

// NewLazyYAMLTree builds a lazy YAML tree.
func NewLazyYAMLTree[R Directive](name, template string, directives ...R) (Tree, error) {
	return NewLazyTree(driver.NewYAMLDriver(), name, template, directives...)
}

// NewLazyInstantYAMLTree builds a lazy instant YAML tree.
func NewLazyInstantYAMLTree[R Directive](name, template string, directives ...R) (Tree, error) {
	return NewLazyInstantTree(driver.NewYAMLDriver(), name, template, directives...)
}

// NewLazyCacheYAMLTree builds a lazy YAML tree with cache TTL.
func NewLazyCacheYAMLTree[R Directive](name, template string, ttl time.Duration, directives ...R) (Tree, error) {
	return NewLazyCacheTree(driver.NewYAMLDriver(), name, template, ttl, directives...)
}

// NewTileTree builds a tile tree.
func NewTileTree[R Directive](name, template string, directives ...R) (Tree, error) {
	return NewTree(driver.NewTileDriver(), name, template, directives...)
}

// NewLazyTileTree builds a lazy tile tree.
func NewLazyTileTree[R Directive](name, template string, directives ...R) (Tree, error) {
	return NewLazyTree(driver.NewTileDriver(), name, template, directives...)
}

// NewLazyInstantTileTree builds a lazy instant tile tree.
func NewLazyInstantTileTree[R Directive](name, template string, directives ...R) (Tree, error) {
	return NewLazyInstantTree(driver.NewTileDriver(), name, template, directives...)
}

// NewLazyCacheTileTree builds a lazy cache tile tree.
func NewLazyCacheTileTree[R Directive](name, template string, ttl time.Duration, directives ...R) (Tree, error) {
	return NewLazyCacheTree(driver.NewTileDriver(), name, template, ttl, directives...)
}

// NewXMLTree builds an XML tree.
func NewXMLTree[R Directive](name, template string, directives ...R) (Tree, error) {
	return NewTree(driver.NewXMLDriver(), name, template, directives...)
}

// NewLazyXMLTree builds a lazy XML tree.
func NewLazyXMLTree[R Directive](name, template string, directives ...R) (Tree, error) {
	return NewLazyTree(driver.NewXMLDriver(), name, template, directives...)
}

// NewLazyInstantXMLTree builds a lazy instant XML tree.
func NewLazyInstantXMLTree[R Directive](name, template string, directives ...R) (Tree, error) {
	return NewLazyInstantTree(driver.NewXMLDriver(), name, template, directives...)
}

// NewLazyCacheXMLTree builds a lazy XML tree with cache TTL.
func NewLazyCacheXMLTree[R Directive](name, template string, ttl time.Duration, directives ...R) (Tree, error) {
	return NewLazyCacheTree(driver.NewXMLDriver(), name, template, ttl, directives...)
}

// NewTOMLTree builds a TOML tree.
func NewTOMLTree[R Directive](name, template string, directives ...R) (Tree, error) {
	return NewTree(driver.NewTOMLDriver(), name, template, directives...)
}

// NewLazyTOMLTree builds a lazy TOML tree.
func NewLazyTOMLTree[R Directive](name, template string, directives ...R) (Tree, error) {
	return NewLazyTree(driver.NewTOMLDriver(), name, template, directives...)
}

// NewLazyInstantTOMLTree builds a lazy instant TOML tree.
func NewLazyInstantTOMLTree[R Directive](name, template string, directives ...R) (Tree, error) {
	return NewLazyInstantTree(driver.NewTOMLDriver(), name, template, directives...)
}

// NewLazyCacheTOMLTree builds a lazy TOML tree with cache TTL.
func NewLazyCacheTOMLTree[R Directive](name, template string, ttl time.Duration, directives ...R) (Tree, error) {
	return NewLazyCacheTree(driver.NewTOMLDriver(), name, template, ttl, directives...)
}

// NewTree builds a standard tree.
func NewTree[R Directive](driver driver.Driver, name, template string, directives ...R) (Tree, error) {
	return buildTree(newTree[R](driver, name, template), toA(directives...)...)
}

// NewLazyTree builds a lazy tree.
func NewLazyTree[R Directive](driver driver.Driver, name, template string, directives ...R) (Tree, error) {
	return buildTree(newTree[R](driver, name, template).lazy(), toA(directives...)...)
}

// NewLazyInstantTree builds a lazy instant tree.
func NewLazyInstantTree[R Directive](driver driver.Driver, name, template string, directives ...R) (Tree, error) {
	return buildTree(newTree[R](driver, name, template).lazy().instant(), toA(directives...)...)
}

// NewLazyCacheTree builds a lazy tree with cache TTL.
// After the TTL expires, the next Get() triggers re-realization.
func NewLazyCacheTree[R Directive](driver driver.Driver, name, template string, ttl time.Duration, directives ...R) (Tree, error) {
	return buildTree(newTree[R](driver, name, template).lazy().cache(ttl), toA(directives...)...)
}

func newTree[R Directive](diver driver.Driver, name, template string) *tree {
	return &tree{
		name: name,

		defaultCtx: &driver.RealizeContext{Context: context.Background()},

		content:  []byte(template),
		driver:   diver,
		children: make(map[string]Tree),
	}
}
func buildTree(tree *tree, directives ...Directive) (Tree, error) {
	if err := tree.build(directives...); err != nil {
		return nil, fmt.Errorf("build tree fail: %w", err)
	}
	return tree, nil
}
func toA[R Directive](directives ...R) (arr []Directive) {
	for _, d := range directives {
		arr = append(arr, d)
	}
	return
}

func NewDirective(path string, Processors ...driver.Processor) Directive {
	return &directive{path, Processors}
}
