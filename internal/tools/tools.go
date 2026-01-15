package tools

import (
	"strings"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

func Title(str string) string {
	if len(str) < 1 {
		return str
	}
	f := str[0:1]
	return cases.Title(language.English, cases.Compact).String(f) + str[1:]
}

func HasAnyPrefix(s string, options []string) bool {
	for _, o := range options {
		if strings.HasPrefix(s, o) {
			return true
		}
	}
	return false
}

func Collect[A, B any](s []A, mapper func(A) B) []B {
	r := make([]B, len(s))
	for i, a := range s {
		r[i] = mapper(a)
	}
	return r
}

func CollectValues[K comparable, A any, B any](m map[K]A, mapper func(A) B) []B {
	r := make([]B, len(m))
	i := 0
	for _, a := range m {
		r[i] = mapper(a)
		i++
	}
	return r
}

func GrepValues[K comparable, V any](m map[K]V, predicate func(V) bool) []V {
	r := []V{}
	for _, a := range m {
		if predicate(a) {
			r = append(r, a)
		}
	}
	return r
}

func BoolPtr(b bool) *bool {
	return &b
}

func OrPtr(b *bool, def *bool) *bool {
	if b != nil {
		return b
	}
	return def
}

func ZerofPtr() *float64 {
	var f float64 = 0
	return &f
}

func Append[E any](s []E, elem E) []E {
	c := make([]E, len(s)+1)
	copy(c, s)
	c[len(s)] = elem
	return c
}

func Index[K comparable, V any](s []V, indexer func(V) K) map[K]V {
	m := map[K]V{}
	for _, v := range s {
		k := indexer(v)
		m[k] = v
	}
	return m
}

func IndexMany[K comparable, V any](s []V, indexer func(V) K) map[K][]V {
	m := map[K][]V{}
	for _, v := range s {
		k := indexer(v)
		a, ok := m[k]
		if !ok {
			a = []V{}
		}
		a = append(a, v)
		m[k] = a
	}
	return m
}

func MapReduce[A any, B any, C any](s []A, mapper func(A) (B, bool, error), reducer func([]B) (C, error)) (C, error) {
	mapped := []B{}
	for _, a := range s {
		if b, ok, err := mapper(a); err != nil {
			var z C
			return z, err
		} else if ok {
			mapped = append(mapped, b)
		}
	}
	return reducer(mapped)
}

func Article(str string) string {
	if Voweled(str) {
		return "an"
	} else {
		return "a"
	}
}

func Voweled(str string) bool {
	if len(str) < 1 {
		return false
	}
	c, _ := utf8.DecodeRuneInString(str)
	switch c {
	case 'a', 'i', 'e', 'o', 'u', 'y', 'A', 'I', 'E', 'O', 'U', 'Y':
		return true
	}
	return false
}

func Singularize(str string) string {
	if strings.HasSuffix(str, "ies") {
		return str[0:len(str)-3] + "y"
	}
	if strings.HasSuffix(str, "es") {
		return str[0 : len(str)-2]
	}
	if strings.HasSuffix(str, "s") {
		return str[0 : len(str)-1]
	}
	return str
}
