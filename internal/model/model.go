package model

import (
	"go/ast"
	"go/token"
	"io"
	"slices"
)

type Field struct {
	Name       string
	Attr       string
	Type       Type
	Tag        string
	Summary    string
	Required   *bool
	InRequest  bool
	InResponse bool
}

var (
	Required    = boolPtr(true)
	NotRequired = boolPtr(false)
)

func NewField(name string, attr string, t Type, tag string, summary string, req *bool, inRequest bool, inResponse bool) Field {
	return Field{
		Name:       name,
		Attr:       attr,
		Type:       t,
		Tag:        tag,
		Summary:    summary,
		Required:   req,
		InRequest:  inRequest,
		InResponse: inResponse,
	}
}

func HasRequestExceptions(t Type) bool {
	for _, f := range t.Fields() {
		if !f.InRequest {
			return true
		}
	}
	return false
}

func HasResponseExceptions(t Type) bool {
	for _, f := range t.Fields() {
		if !f.InResponse {
			return true
		}
	}
	return false
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
	Required() *bool
}

type ResponseHeaderDesc struct {
	Summary  string
	Required bool
	Explode  bool
	Examples map[string]string
}

type DefaultResponseHeaderDesc struct {
	Summary   string
	Required  bool
	Explode   bool
	Examples  map[string]string
	OnlyCodes []int
	OnError   bool
	OnSuccess bool
}

func (h DefaultResponseHeaderDesc) IsApplicable(code int) bool {
	if h.OnlyCodes != nil && !slices.Contains(h.OnlyCodes, code) {
		return false
	}
	if code < 300 && !h.OnSuccess {
		return false
	}
	if code >= 400 && !h.OnError {
		return false
	}
	return true
}

type RequestHeaderDesc struct {
	Name        string
	Description string
	Required    bool
	Examples    map[string]string
}

type Endpoint struct {
	Verb string
	Path string
	Fun  string
}

type Resp struct {
	Summary string
	Type    Type
}

type Param struct {
	Name        string
	Description string
	Type        Type
}

type InferredSummary struct {
	Object         string
	Child          string
	Action         string
	Adjective      string
	ForAccount     bool
	ForAllAccounts bool
	SpecificObject bool
	SpecificChild  bool
}

type Impl struct {
	Endpoint        Endpoint
	Source          string
	Filename        string
	Line            int
	Column          int
	Fun             *ast.FuncDecl
	Name            string
	Comments        []string
	Resp            map[int]Resp
	QueryParams     []Param
	PathParams      []Param
	HeaderParams    []Param
	BodyParams      []Param
	Tags            []string
	Summary         string
	Description     string
	InferredSummary InferredSummary
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
	DefaultResponseHeaders    map[string]DefaultResponseHeaderDesc
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

func boolPtr(b bool) *bool {
	return &b
}
