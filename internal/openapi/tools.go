package openapi

import (
	"slices"
	"strings"

	"github.com/pb33f/libopenapi/orderedmap"
	"go.yaml.in/yaml/v4"
)

var verbSortOrder = []string{
	"GET",
	"QUERY",
	"HEAD",
	"POST",
	"PUT",
	"DELETE",
	"PATCH",
	"OPTIONS",
	"TRACE",
}

func verbSort(a, b string) int {
	i := slices.Index(verbSortOrder, a)
	j := slices.Index(verbSortOrder, b)
	if i >= 0 {
		if j >= 0 {
			return i - j
		} else {
			return -1
		}
	} else {
		if j >= 0 {
			return 1
		} else {
			return strings.Compare(a, b)
		}
	}
}

func find[E any](s []E, matcher func(E) bool) (E, bool) {
	for _, e := range s {
		if matcher(e) {
			return e, true
		}
	}
	var zero E
	return zero, false
}

func merge[K comparable, V any](dst **orderedmap.Map[K, V], src *orderedmap.Map[K, V]) {
	if src == nil || dst == nil {
		return
	}
	if *dst == nil {
		*dst = src
		return
	}
	for k := range src.KeysFromOldest() {
		if _, ok := (*dst).Get(k); !ok {
			if v, ok := src.Get(k); ok {
				(*dst).Set(k, v)
			}
		}
	}
}

func ext(pairs ...string) *orderedmap.Map[string, *yaml.Node] {
	result := orderedmap.New[string, *yaml.Node]()
	for i := 0; i < len(pairs); i += 2 {
		k := pairs[i]
		v := pairs[i+1]
		result.Set(k, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v})
	}
	return result
}

func somap[V any]() *orderedmap.Map[string, V] {
	return orderedmap.New[string, V]()
}

func omap1[K comparable, V any](k K, v V) *orderedmap.Map[K, V] {
	m := orderedmap.New[K, V]()
	m.Set(k, v)
	return m
}

func ydict(parent *yaml.Node, id string, pairs ...string) {
	c := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	{
		k := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: id}
		parent.Content = append(parent.Content, k, c)
	}

	for i := 0; i < len(pairs); i += 2 {
		k := pairs[i]
		v := pairs[i+1]
		kn := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: k}
		vn := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}
		c.Content = append(c.Content, kn, vn)
	}
}
