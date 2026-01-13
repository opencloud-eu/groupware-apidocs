package model

import (
	"go/ast"
	"go/token"
	"io"
	"regexp"
)

type Field struct {
	Name    string
	Attr    string
	Type    Type
	Tag     string
	Summary string
}

var tagRegex = regexp.MustCompile(`json:"(.+?)(?:,(omitempty|omitzero))?"`)

func NewField(name string, t Type, tag string, summary string) (Field, bool) {
	attr := ""
	if m := tagRegex.FindAllStringSubmatch(tag, 2); m != nil {
		attr = m[0][1]
	}
	if attr == "-" {
		return Field{}, false
	}
	if attr == "" {
		attr = name
	}
	return Field{
		Name:    name,
		Attr:    attr,
		Type:    t,
		Tag:     tag,
		Summary: summary,
	}, true
}

type Type interface {
	Key() string
	Name() string
	IsBasic() bool
	IsArray() bool
	IsMap() bool
	Deref() (Type, bool)
	String() string
	Fields() []Field
	Element() (Type, bool)
	Summary() string
	Description() string
	Pos() (token.Position, bool)
}

type ResponseHeaderDesc struct {
	Summary  string
	Required bool
	Explode  bool
}

type RequestHeaderDesc struct {
	Name        string
	Description string
	Required    bool
}

type Endpoint struct {
	Verb string
	Path string
	Fun  string
}

type Resp struct {
	Type Type
}

type Param struct {
	Name        string
	Description string
	Required    bool
	Type        Type
}

type Impl struct {
	Endpoint     Endpoint
	Source       string
	Line         int
	Fun          *ast.FuncDecl
	Name         string
	Comments     []string
	Resp         map[int]Resp
	QueryParams  []Param
	PathParams   []Param
	HeaderParams []Param
	BodyParams   []Param
	Tags         []string
	Summary      string
	Description  string
}

type Undocumented struct {
	Pos      token.Position
	Endpoint Endpoint
}

type Example struct {
	Key    string
	Title  string
	Text   string
	Origin string
}

type Examples struct {
	Key            string
	DefaultExample Example
	RequestExample *Example
}

func (r Examples) ForRequest() (Example, bool) {
	if r.RequestExample != nil {
		return *r.RequestExample, true
	}
	return r.DefaultExample, false
}

func (r Examples) ForParameter() (Example, bool) {
	return r.DefaultExample, false
}

func (r Examples) ForResponse() (Example, bool) {
	return r.DefaultExample, false
}

type Model struct {
	Routes                    []Endpoint
	PathParams                map[string]Param
	QueryParams               map[string]Param
	HeaderParams              map[string]Param
	Impls                     []Impl
	Types                     []Type
	Examples                  map[string]Examples
	Enums                     map[string][]string
	DefaultResponses          map[int]Type
	DefaultResponseHeaders    map[string]ResponseHeaderDesc
	CommonRequestHeaders      []RequestHeaderDesc
	UndocumentedResults       map[string]Undocumented
	UndocumentedRequestBodies map[string]Undocumented
}

func (m Model) ResolveType(k string) (Type, bool) {
	for _, t := range m.Types {
		if t.Key() == k {
			return t, true
		}
	}
	return nil, false
}

type Sink interface {
	Output(model Model, w io.Writer) error
}
