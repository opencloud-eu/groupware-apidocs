package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	base "github.com/pb33f/libopenapi/datamodel/high/base"
	highbase "github.com/pb33f/libopenapi/datamodel/high/base"
	v3 "github.com/pb33f/libopenapi/datamodel/high/v3"
	"github.com/pb33f/libopenapi/orderedmap"
	"go.yaml.in/yaml/v4"
)

var (
	openIdConnectUrl = "https://keycloak.opencloud.test/realms/openCloud/.well-known/openid-configuration"
	includeBasicAuth = true
	contact          = &base.Contact{
		Name:  "Pascal Bleser",
		Email: "p.bleser@opencloud.eu",
	}
)

type OpenApiSink struct {
}

var _ Sink = OpenApiSink{}

func (s OpenApiSink) newPathItem(_ string, _ string, _ Impl) *v3.PathItem {
	return &v3.PathItem{}
}

func (s OpenApiSink) newOperation(id string, _ string, _ string, im Impl) *v3.Operation {
	return &v3.Operation{
		OperationId: id,
		Summary:     im.Summary,
		Description: im.Description,
	}
}

var (
	SchemaComponentRefPrefix   = "#/components/schemas/"
	ResponseComponentRefPrefix = "#/components/responses/"
)

var (
	objs          = regexp.MustCompile(`^([^/]+?s)/?$`)
	objById       = regexp.MustCompile(`^([^/]+?)s/{[^/]*?id}$`)
	objsInObjById = regexp.MustCompile(`^([^/]+?)s/{[^/]*?id}/([^/]+?)s$`)
	apiTag        = regexp.MustCompile(`^\s*@api:tags?\s+(.+)\s*$`)
)

var (
	anySchema             = &base.Schema{Type: []string{"object"}}
	objectSchema          = &base.Schema{Type: []string{"object"}}
	stringSchema          = &base.Schema{Type: []string{"string"}}
	integerSchema         = &base.Schema{Type: []string{"integer"}}
	unsignedIntegerSchema = &base.Schema{Type: []string{"integer"}, Minimum: zerofPtr()}
	booleanSchema         = &base.Schema{Type: []string{"boolean"}}
	stringArraySchema     = &base.Schema{
		Type:  []string{"array"},
		Items: &base.DynamicValue[*base.SchemaProxy, bool]{N: 0, A: base.CreateSchemaProxy(stringSchema)},
	}
	timeSchema        = &base.Schema{Type: []string{"string"}, Format: "date-time"}
	patchObjectSchema = &base.Schema{
		Type:                 []string{"object"},
		AdditionalProperties: &base.DynamicValue[*base.SchemaProxy, bool]{N: 0, A: base.CreateSchemaProxy(anySchema)},
	}

	// numberSchema = &base.Schema{Type: []string{"number"}}
	// dateSchema = &base.Schema{Type: []string{"string"}, Format: "date"}
)

func (s OpenApiSink) parameterize(p Param, in string, requiredByDefault bool, model map[string]Param) (*v3.Parameter, error) {
	if g, ok := model[p.Name]; ok {
		req := requiredByDefault
		if p.Required {
			req = true
		}
		if !req && g.Required {
			req = true
		}
		desc := p.Description
		if desc == "" {
			desc = g.Description
		}
		return &v3.Parameter{
			Name:        g.Name,
			In:          in,
			Required:    &req,
			Description: desc,
		}, nil
	} else {
		return nil, fmt.Errorf("failed to resolve %s parameter '%s'", in, p.Name)
	}
}

func (s OpenApiSink) Output(model Model, w io.Writer) error {
	typeMap := index(model.Types, func(t Type) string { return t.Key() })
	imMap := index(model.Impls, func(i Impl) string { return i.Name })
	paths := indexMany(model.Routes, func(r Endpoint) string { return r.Path })

	pathItemMap := orderedmap.New[string, *v3.PathItem]()
	schemaComponentTypes := map[string]Type{} // collects items that need to be documented in /components/schemas
	for path, endpoints := range paths {
		var pathItem *v3.PathItem = nil

		for _, r := range endpoints {
			im, ok := imMap[r.Fun]
			if !ok {
				return fmt.Errorf("verb='%s' path='%s' fun='%s': failed to find function in imMap for '%s'", r.Verb, r.Path, r.Fun, r.Fun)
			}

			if pathItem == nil {
				pathItem = s.newPathItem(r.Verb, path, im)
			}

			opid := r.Fun
			op := s.newOperation(opid, r.Verb, path, im)

			// path parameters
			for _, p := range im.PathParams {
				if param, err := s.parameterize(p, "path", true, model.PathParams); err != nil {
					return fmt.Errorf("verb='%s' path='%s' fun='%s': %s", r.Verb, r.Path, r.Fun, err)
				} else {
					op.Parameters = append(op.Parameters, param)
				}
			}

			// query parameters
			for _, p := range im.QueryParams {
				if param, err := s.parameterize(p, "query", false, model.QueryParams); err != nil {
					return fmt.Errorf("verb='%s' path='%s' fun='%s': %s", r.Verb, r.Path, r.Fun, err)
				} else {
					op.Parameters = append(op.Parameters, param)
				}
			}

			// header parameters
			for _, p := range im.HeaderParams {
				if param, err := s.parameterize(p, "header", false, model.HeaderParams); err != nil {
					return fmt.Errorf("verb='%s' path='%s' fun='%s': %s", r.Verb, r.Path, r.Fun, err)
				} else {
					op.Parameters = append(op.Parameters, param)
				}
			}

			// common header parameters
			for _, h := range model.CommonRequestHeaders {
				req := false
				if h.Required {
					req = true
				}
				schemaRef := highbase.CreateSchemaProxy(stringSchema)
				op.Parameters = append(op.Parameters, &v3.Parameter{
					Name:        h.Name,
					In:          "header",
					Schema:      schemaRef,
					Description: h.Description,
					Required:    &req,
				})
			}

			// body parameters
			if requestBody, err := s.bodyparams(im.BodyParams, im, typeMap, schemaComponentTypes); err != nil {
				return err
			} else if requestBody != nil {
				op.RequestBody = requestBody
			}

			// responses
			if responses, err := s.responses(im, model, typeMap, schemaComponentTypes); err != nil {
				return err
			} else if responses != nil {
				op.Responses = responses
			} else {
				log.Printf("impl has no responses: %#v", im) // for debugging
			}

			op.Tags = im.Tags
			if strings.HasPrefix(path, "/accounts/all/") || path == "/accounts/all" {
				op.Tags = append(op.Tags, "unified")
			}

			op.Extensions = ext1("x-oc-source", fmt.Sprintf("%s:%d", im.Source, im.Line))

			if err := s.assign(op, r, pathItem); err != nil {
				return err
			}
		}
		if pathItem != nil {
			pathItemMap.Set(path, pathItem)
		}
	}

	componentResponses := orderedmap.New[string, *v3.Response]()
	{
		for code, t := range model.DefaultResponses {
			if t == nil {
				continue
			}
			key := fmt.Sprintf("%s.%d", t.Key(), code)
			contentMap := orderedmap.New[string, *v3.MediaType]()
			if ref, err := s.refType(t, model); err != nil {
				return fmt.Errorf("failed to reference default response type %s: %v", t, err)
			} else {
				contentMap.Set("application/json", &v3.MediaType{
					Schema: base.CreateSchemaProxyRef(SchemaComponentRefPrefix + ref),
				})
				schemaComponentTypes[ref] = t
			}
			// TODO extract default response summary and description from @api
			summary := ""
			description := ""

			if summary == "" {
				summary = http.StatusText(code)
			}
			response := &v3.Response{
				Summary:     summary,
				Description: description,
				Content:     contentMap,
			}
			componentResponses.Set(key, response)
		}
	}

	// add the schema of types that are referenced as body parameters or responses
	componentSchemas := orderedmap.New[string, *highbase.SchemaProxy]()
	{
		// since each type may reference other types (typically struct fields), we have to loop
		// indefinitely until we don't have any additional types to document that haven't been
		// documented yet
		{
			maxIterations := 20 // just to avoid an infinite loop
			i := 0
			for ; i < maxIterations && len(schemaComponentTypes) > 0; i++ {
				// keep track of other types that are referenced within those types
				moreSchemaComponentTypes := map[string]Type{}
				for _, t := range schemaComponentTypes {
					if strings.HasPrefix(t.Name(), "Swagger") {
						continue
					}
					ctx := fmt.Sprintf("resolving schema component type '%s'", t.Key())
					if schema, _, err := s.schematize(ctx, t, []string{}, typeMap, moreSchemaComponentTypes); err == nil {
						if schema != nil {
							componentSchemas.Set(t.Key(), schema)
						}
					} else {
						return err
					}
				}
				schemaComponentTypes = moreSchemaComponentTypes
			}
			if i >= maxIterations {
				log.Panicf("documenting the schemas of types has iterated %d times, which is more than the limit of %d", i, maxIterations)
			}
		}
	}

	componentHeaders := orderedmap.New[string, *v3.Header]()
	{
		for name, desc := range model.DefaultResponseHeaders {
			req := false
			if desc.Required {
				req = true
			}
			explode := false
			if desc.Explode {
				explode = true
			}
			componentHeaders.Set(name, &v3.Header{
				Description: desc.Summary,
				Schema:      highbase.CreateSchemaProxy(stringSchema),
				Required:    req,
				Explode:     explode,
			})
		}
	}

	components := &v3.Components{
		Schemas:   componentSchemas,
		Responses: componentResponses,
		Headers:   componentHeaders,
	}

	var securitySchemes *orderedmap.Map[string, *v3.SecurityScheme] = orderedmap.New[string, *v3.SecurityScheme]()
	{
		securitySchemes.Set("oidc", &v3.SecurityScheme{
			Type:             "openIdConnect",
			Description:      "Authentication for API Calls via OIDC",
			OpenIdConnectUrl: openIdConnectUrl,
		})
		if includeBasicAuth {
			securitySchemes.Set("basic", &v3.SecurityScheme{
				Type:        "http",
				Scheme:      "basic",
				Description: "Basic Authentication for API Calls, if enabled",
			})
		}
	}
	components.SecuritySchemes = securitySchemes

	doc := &v3.Document{
		Version: "0.0.0",
		Info: &base.Info{
			Title:   "OpenCloud Groupware API",
			Contact: contact,
		},
		Paths: &v3.Paths{
			PathItems: pathItemMap,
		},
		Components: components,
	}
	{
		security := []*highbase.SecurityRequirement{}
		for k := range securitySchemes.KeysFromOldest() {
			security = append(security, &highbase.SecurityRequirement{
				Requirements: omap1(k, []string{}),
			})
		}
		doc.Security = security
	}

	if rendered, err := doc.Render(); err == nil {
		if _, err := w.Write(rendered); err != nil {
			return err
		}
	} else {
		return err
	}
	return nil
}

func (s OpenApiSink) schematize(ctx string, t Type, path []string, typeMap map[string]Type, schemaComponentTypes map[string]Type) (*highbase.SchemaProxy, *orderedmap.Map[string, *yaml.Node], error) {
	if t == nil {
		return nil, nil, fmt.Errorf("schematize: t is nil (path=%s)", strings.Join(path, " > "))
	}
	isPatchObject := strings.Contains(t.Key(), "Patch") || strings.Contains(t.String(), "Patch")
	var _ = isPatchObject
	if elt, ok := t.Element(); ok {
		if t.IsMap() {
			if deref, ext, err := s.schematize(ctx, elt, sappend(path, t.Name()), typeMap, schemaComponentTypes); err != nil {
				return nil, nil, err
			} else {
				schema := &highbase.Schema{
					Type:                 []string{"object"},
					AdditionalProperties: &base.DynamicValue[*base.SchemaProxy, bool]{N: 0, A: deref},
					Extensions:           ext,
				}
				return highbase.CreateSchemaProxy(schema), nil, nil
			}
		} else if t.IsArray() {
			if deref, ext, err := s.schematize(ctx, elt, sappend(path, t.Name()), typeMap, schemaComponentTypes); err != nil {
				return nil, nil, err
			} else {
				schema := &highbase.Schema{
					Type:       []string{"array"},
					Items:      &base.DynamicValue[*base.SchemaProxy, bool]{N: 0, A: deref},
					Extensions: ext,
				}
				return highbase.CreateSchemaProxy(schema), nil, nil
			}
		} else {
			return nil, nil, fmt.Errorf("failed to schematize type: has Element() but is neither map nor array: %#v", t)
		}
	}
	d, ok := t.Deref()
	if !ok {
		d = t
	}
	if d.Name() == "PatchObject" {
		ext := ext1("x-oc-type-source-basic", d.Key())
		return highbase.CreateSchemaProxy(patchObjectSchema), ext, nil
	}
	if d.IsBasic() {
		ext := ext1("x-oc-type-source-basic", d.Key())
		switch d.Key() {
		case "io.Closer":
			return nil, ext, nil // TODO streaming
		case "error":
			return nil, ext, nil // TODO where is error even referenced?
		case "time.Time":
			return highbase.CreateSchemaProxy(timeSchema), ext, nil
		case "string":
			return highbase.CreateSchemaProxy(stringSchema), ext, nil
		case "int":
			return highbase.CreateSchemaProxy(integerSchema), ext, nil
		case "uint":
			return highbase.CreateSchemaProxy(unsignedIntegerSchema), ext, nil
		case "bool":
			return highbase.CreateSchemaProxy(booleanSchema), ext, nil
		case "any":
			return highbase.CreateSchemaProxy(anySchema), ext, nil
		default:
			return nil, ext, fmt.Errorf("failed to schematize built-in type '%s'/'%s' %#v", t.Key(), d.Key(), t)
		}
	}

	props := orderedmap.New[string, *highbase.SchemaProxy]()
	if len(path) == 0 {
		for _, f := range d.Fields() {
			ctx := fmt.Sprintf("%s.%s", ctx, f.Attr)
			if fs, _, err := s.schematize(ctx, f.Type, sappend(path, t.Name()), typeMap, schemaComponentTypes); err != nil {
				return nil, nil, err
			} else if fs != nil {
				props.Set(f.Attr, fs)
			}
		}
		schema := &highbase.Schema{
			Type:       []string{"object"},
			Properties: props,
			Title:      strings.Join(path, " > "), // for debugging
		}
		return highbase.CreateSchemaProxy(schema), ext1("x-oc-type-source-struct", t.Key()), nil
	} else {
		// use a reference to avoid circular references and endless loops
		ref := d.Key()
		if t, ok := typeMap[ref]; ok {
			schemaComponentTypes[ref] = t
		} else {
			return nil, nil, fmt.Errorf("schematize: failed to find referenced type in typeMap: %s", ref)
		}
		return highbase.CreateSchemaProxyRef(SchemaComponentRefPrefix + ref), ext1("x-oc-type-source-ref", t.Key()), nil
	}
}

func (s OpenApiSink) reqschema(ref string, im Impl, typeMap map[string]Type, schemaComponentTypes map[string]Type) (*base.SchemaProxy, *orderedmap.Map[string, *yaml.Node], error) {
	if t, ok := typeMap[ref]; ok {
		return s.schematize("", t, []string{im.Name}, typeMap, schemaComponentTypes)
	}

	var schemaRef *base.SchemaProxy = nil
	var ext *orderedmap.Map[string, *yaml.Node] = nil
	switch ref {
	case "map[string]any":
		schemaRef = base.CreateSchemaProxy(objectSchema)
		ref = ""
	case "string":
		schemaRef = base.CreateSchemaProxy(stringSchema)
		ref = ""
	case "[]string":
		schemaRef = base.CreateSchemaProxy(stringArraySchema)
		ref = ""
	case "any":
		schemaRef = base.CreateSchemaProxy(anySchema)
		ref = ""
	default:
		return nil, nil, fmt.Errorf("reqschema: failed to schematize unsupported response type '%s' for endpoint '%s %s' in function '%s'", ref, im.Endpoint.Verb, im.Endpoint.Path, im.Name)
		//schemaRef = base.CreateSchemaProxyRef(SchemaComponentRefPrefix + ref)
		//ext = ext1("x-oc-ref-source", "bodyparam of "+im.Name)
	}
	if ref != "" {
		if t, ok := typeMap[ref]; ok {
			schemaComponentTypes[ref] = t
		} else {
			return nil, nil, fmt.Errorf("verb='%s' path='%s' fun='%s': failed to find type definition in type map for request body '%s'", im.Endpoint.Verb, im.Endpoint.Path, im.Endpoint.Fun, ref)
		}
	}
	return schemaRef, ext, nil
}

func (s OpenApiSink) bodyparams(refs []string, im Impl, typeMap map[string]Type, schemaComponentTypes map[string]Type) (*v3.RequestBody, error) {
	var schemaRef *highbase.SchemaProxy
	var err error
	switch len(refs) {
	case 0:
		return nil, nil
	case 1:
		schemaRef, _, err = s.reqschema(refs[0], im, typeMap, schemaComponentTypes)
	default:
		schemaRef, err = mapReduce(refs, func(ref string) (*highbase.SchemaProxy, bool, error) {
			schemaRef, _, err := s.reqschema(ref, im, typeMap, schemaComponentTypes)
			return schemaRef, true, err
		}, func(schemas []*highbase.SchemaProxy) (*highbase.SchemaProxy, error) {
			return base.CreateSchemaProxy(&base.Schema{OneOf: schemas}), nil
		})
		if err != nil {
			return nil, err
		}
	}
	return &v3.RequestBody{
		Required:   boolPtr(true),
		Content:    omap1("application/json", &v3.MediaType{Schema: schemaRef}),
		Extensions: ext1("x-oc-ref-source", "bodyparams of "+im.Name),
	}, nil
}

func (s OpenApiSink) responses(im Impl, model Model, _ map[string]Type, schemaComponentTypes map[string]Type) (*v3.Responses, error) {
	respMap := orderedmap.New[string, *v3.Response]()
	for code, resp := range im.Resp {
		contentMap := orderedmap.New[string, *v3.MediaType]()
		if resp.Type != nil {
			if ref, err := s.refType(resp.Type, model); err != nil {
				return nil, fmt.Errorf("verb='%s' path='%s' fun='%s': failed to reference response type for %s: %v", im.Endpoint.Verb, im.Endpoint.Path, im.Endpoint.Fun, im.Name, err)
			} else {
				// use a reference for the response type
				contentMap.Set("application/json", &v3.MediaType{
					Schema:     base.CreateSchemaProxyRef(SchemaComponentRefPrefix + ref),
					Extensions: ext1("x-oc-ref-source", fmt.Sprintf("response type %s", resp.Type)),
				})
				schemaComponentTypes[ref] = resp.Type
			}
		} else {
			// when Type is nil, it means that there is no response object, used with 204 No Content,
			// but we still have to add the Response object for that code below
		}

		// TODO extract response summary and description from apidoc annotation
		summary := ""
		if summary == "" {
			summary = http.StatusText(code)
		}
		description := ""

		respMap.Set(strconv.Itoa(code), &v3.Response{
			Summary:     summary,
			Description: description,
			Content:     contentMap,
			Extensions:  ext1("x-of-source", fmt.Sprintf("responses of %s:%d", im.Name, im.Line)),
		})
	}
	// also add the default responses in every operation
	for code, respType := range model.DefaultResponses {
		codeKey := strconv.Itoa(code)
		if _, ok := respMap.Get(codeKey); ok {
			// that code is already defined for that function, which overrides the generic default
			// response for that code, so don't do anything here
		} else {
			if respType != nil {
				ref := fmt.Sprintf("%s.%d", respType.Key(), code)
				respMap.Set(codeKey, &v3.Response{
					Reference: ResponseComponentRefPrefix + ref,
				})
			} else {
				respMap.Set(codeKey, &v3.Response{
					Summary: http.StatusText(code),
				})
			}
		}
	}
	return &v3.Responses{Codes: respMap}, nil
}

func (s OpenApiSink) assign(op *v3.Operation, r Endpoint, pathItem *v3.PathItem) error {
	switch r.Verb {
	case "GET":
		pathItem.Get = op
	case "PUT":
		pathItem.Put = op
	case "POST":
		pathItem.Post = op
	case "DELETE":
		pathItem.Delete = op
	case "PATCH":
		pathItem.Patch = op
	case "OPTIONS":
		pathItem.Options = op
	case "QUERY":
		pathItem.Query = op
	case "HEAD":
		pathItem.Head = op
	case "TRACE":
		pathItem.Trace = op
	default:
		if pathItem.AdditionalOperations == nil {
			pathItem.AdditionalOperations = orderedmap.New[string, *v3.Operation]()
		}
		pathItem.AdditionalOperations.Set(r.Verb, op)
	}
	return nil
}

func (s OpenApiSink) refType(t Type, _ Model) (string, error) {
	if t == nil {
		return "", errors.New("t is nil")
	}
	return t.String(), nil
}

func ext1(k string, v string) *orderedmap.Map[string, *yaml.Node] {
	ext := orderedmap.New[string, *yaml.Node]()
	ext.Set(k, &yaml.Node{Kind: yaml.ScalarNode, Value: v})
	return ext
}
