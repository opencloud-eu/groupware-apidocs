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

func HasExceptions(t Type) bool {
	return HasRequestExceptions(t) || HasResponseExceptions(t)
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
}

type ResponseHeaderDesc struct {
	Summary  string
	Required bool
	Explode  bool
	Examples map[string]string
}

type DefaultResponseDesc struct {
	Summary string
	Type    Type
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
	Exploded    bool
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

type ParamDefinition struct {
	Name        string
	Description string
}

func NewParamDefinition(name string, description string) ParamDefinition {
	return ParamDefinition{
		Name:        name,
		Description: description,
	}
}

type Param struct {
	Name        string
	Description string
	Type        Type
	Required    bool
	Exploded    bool
}

func NewParam(name string, description string, t Type, required bool, exploded bool) Param {
	return Param{
		Name:        name,
		Description: description,
		Type:        t,
		Required:    required,
		Exploded:    exploded,
	}
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

type GroupwareError struct {
	Status int
	Code   string
	Title  string
	Detail string
}

type PotentialError struct {
	Name    string
	Type    Type
	Payload GroupwareError
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
	PotentialErrors map[string]PotentialError
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
	TypeId string
	Title  string
	Text   string
	Origin string
}

type Examples struct {
	Key             string
	DefaultExamples []Example
	RequestExamples []Example
}

func (r Examples) ForRequest() ([]Example, bool) {
	if len(r.RequestExamples) > 0 {
		return r.RequestExamples, true
	}
	return r.DefaultExamples, false
}

func (r Examples) ForParameter() ([]Example, bool) {
	return r.DefaultExamples, false
}

func (r Examples) ForResponse() ([]Example, bool) {
	return r.DefaultExamples, true
}

type Model struct {
	Routes                                       []Endpoint
	PathParams                                   map[string]ParamDefinition
	QueryParams                                  map[string]ParamDefinition
	HeaderParams                                 map[string]ParamDefinition
	Impls                                        []Impl
	Types                                        []Type
	Examples                                     map[string]Examples
	Enums                                        map[string][]string
	DefaultResponses                             map[int]DefaultResponseDesc
	DefaultResponseHeaders                       map[string]DefaultResponseHeaderDesc
	CommonRequestHeaders                         []RequestHeaderDesc
	UndocumentedResults                          map[string]Undocumented
	UndocumentedRequestBodies                    map[string]Undocumented
	GlobalPotentialErrors                        []PotentialError
	GlobalPotentialErrorsForQueryParams          []PotentialError
	GlobalPotentialErrorsForMandatoryQueryParams []PotentialError
	GlobalPotentialErrorsForPathParams           []PotentialError
	GlobalPotentialErrorsForMandatoryPathParams  []PotentialError
	GlobalPotentialErrorsForBodyParams           []PotentialError
	GlobalPotentialErrorsForMandatoryBodyParams  []PotentialError
	GlobalPotentialErrorsForAccount              []PotentialError
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
