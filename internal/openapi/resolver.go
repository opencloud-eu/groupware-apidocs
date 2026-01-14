package openapi

import (
	"fmt"
	"maps"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	base "github.com/pb33f/libopenapi/datamodel/high/base"
	highbase "github.com/pb33f/libopenapi/datamodel/high/base"
	v3 "github.com/pb33f/libopenapi/datamodel/high/v3"
	"github.com/pb33f/libopenapi/orderedmap"
	"go.yaml.in/yaml/v4"
	"opencloud.eu/groupware-apidocs/internal/model"
)

type schemaScope string

var (
	schemaScopeRequest  = schemaScope("request")
	schemaScopeResponse = schemaScope("response")
)

type resolver struct {
	basePath           string
	typeMap            map[string]model.Type
	m                  model.Model
	renderedExampleMap map[string]renderedExample
}

func newResolver(basePath string, m model.Model, renderedExampleMap map[string]renderedExample) resolver {
	typeMap := index(m.Types, func(t model.Type) string { return t.Key() })
	return resolver{
		basePath:           basePath,
		typeMap:            typeMap,
		m:                  m,
		renderedExampleMap: renderedExampleMap,
	}
}

func (s resolver) parameterize(p model.Param, in string, requiredByDefault bool, m map[string]model.Param, schemaComponentTypes map[string]model.Type) (*v3.Parameter, error) {
	if g, ok := m[p.Name]; ok {
		req := requiredByDefault
		if p.Type != nil && p.Type.Required() != nil {
			req = *p.Type.Required()
		}
		if !req && g.Type != nil && g.Type.Required() != nil {
			// this is probably never the case
			req = *g.Type.Required()
		}
		desc := p.Description
		if desc == "" {
			desc = g.Description
		}
		var schema *highbase.SchemaProxy
		var ext *orderedmap.Map[string, *yaml.Node]
		if p.Type != nil {
			if s, e, err := s.schematize(schemaScopeRequest, fmt.Sprintf("parameterize(%v, %s)", p, in), p.Type, []string{p.Name}, schemaComponentTypes, desc); err != nil {
				return nil, err
			} else {
				schema = s
				ext = e
			}
		}
		return &v3.Parameter{
			Name:        g.Name,
			In:          in,
			Required:    &req,
			Description: desc,
			Schema:      schema,
			Extensions:  ext,
		}, nil
	} else {
		return nil, fmt.Errorf("failed to resolve %s parameter '%s'", in, p.Name)
	}
}

func (s resolver) schematize(scope schemaScope, ctx string, t model.Type, path []string, schemaComponentTypes map[string]model.Type, desc string) (*highbase.SchemaProxy, *orderedmap.Map[string, *yaml.Node], error) {
	if t == nil {
		return nil, nil, fmt.Errorf("schematize: t is nil (path=%s)", strings.Join(path, " > "))
	}
	if elt, ok := t.Element(); ok {
		if t.IsMap() {
			if deref, ext, err := s.schematize(scope, ctx, elt, sappend(path, t.Name()), schemaComponentTypes, desc); err != nil {
				return nil, nil, err
			} else {
				schema := makeObjectSchema(deref)
				schema.Extensions = ext
				schema.Description = desc
				return highbase.CreateSchemaProxy(schema), nil, nil
			}
		} else if t.IsArray() {
			if deref, ext, err := s.schematize(scope, ctx, elt, sappend(path, t.Name()), schemaComponentTypes, desc); err != nil {
				return nil, nil, err
			} else {
				schema := arraySchema(deref)
				schema.Extensions = ext
				schema.Description = desc
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
			return highbase.CreateSchemaProxy(timeSchema(desc)), ext, nil
		case "string":
			return highbase.CreateSchemaProxy(stringSchema(desc)), ext, nil
		case "int":
			return highbase.CreateSchemaProxy(integerSchema(desc)), ext, nil
		case "uint":
			return highbase.CreateSchemaProxy(unsignedIntegerSchema(desc)), ext, nil
		case "bool":
			return highbase.CreateSchemaProxy(booleanSchema(desc)), ext, nil
		case "any":
			return highbase.CreateSchemaProxy(anySchema(desc)), ext, nil
		default:
			return nil, ext, fmt.Errorf("failed to schematize built-in type '%s'/'%s' %#v", t.Key(), d.Key(), t)
		}
	}

	if len(path) == 0 {
		ext := orderedmap.New[string, *yaml.Node]()

		var examples []*yaml.Node = nil
		var example *model.Example
		{
			if e, ok := s.m.Examples[t.Key()]; ok {
				if rendered, ok := s.renderedExampleMap[t.Key()]; ok {
					var data *yaml.Node
					switch scope {
					case schemaScopeRequest:
						x, _ := e.ForRequest()
						example = &x
						data = rendered.forRequest()
					case schemaScopeResponse:
						x, _ := e.ForResponse()
						example = &x
						data = rendered.forResponse()
					default:
						return nil, nil, fmt.Errorf("schematize: %s: unsupported %T: %v", ctx, scope, scope)
					}
					if data != nil {
						examples = []*yaml.Node{data}
						ext.Set("x-oc-example-scope", &yaml.Node{Kind: yaml.ScalarNode, Value: string(scope)})
					}
				}
			}
		}

		props := orderedmap.New[string, *highbase.SchemaProxy]()
		requiredFields := []string{}
		for _, f := range d.Fields() {
			if scope == schemaScopeRequest && !f.InRequest {
				continue // skip this field
			}
			if scope == schemaScopeResponse && !f.InResponse {
				continue // skip this field
			}

			ctx := fmt.Sprintf("%s.%s", ctx, f.Attr)
			if fs, _, err := s.schematize(scope, ctx, f.Type, sappend(path, t.Name()), schemaComponentTypes, f.Summary); err != nil {
				return nil, nil, err
			} else if fs != nil {
				if f.Attr == "" {
					return nil, nil, fmt.Errorf("schematize: %s: struct property in '%s' has no attr value: %#v", ctx, t.Key(), f)
				}

				// patch in an example if we have one
				if example != nil {
					if err := patchExampleIntoSchema(f.Attr, *example, fs.Schema()); err != nil {
						return nil, nil, fmt.Errorf("schematize: %s: failed to patch example into schema of '%s' property '%s': %w", ctx, t.Key(), f.Attr, err)
					}
				}

				props.Set(f.Attr, fs)
				if f.Required != nil && *f.Required == true {
					requiredFields = append(requiredFields, f.Attr)
				}
			}
		}
		objdesc := desc
		if objdesc == "" {
			objdesc = t.Summary()
			if t.Summary() != "" {
				if t.Description() != "" {
					objdesc = objdesc + "\n" + t.Description()
				}
			} else {
				objdesc = t.Description()
			}
		}

		ext.Set("x-oc-type-source-struct", &yaml.Node{Kind: yaml.ScalarNode, Value: t.Key()})
		if pos, ok := t.Pos(); ok {
			filename := pos.Filename
			if relpath, err := filepath.Rel(s.basePath, filename); err != nil {
				return nil, nil, err
			} else {
				filename = relpath
			}
			ext.Set("x-oc-type-source-pos", &yaml.Node{Kind: yaml.ScalarNode, Value: fmt.Sprintf("%s:%d:%d", filename, pos.Line, pos.Column)})
		}

		schema := &highbase.Schema{
			Type:        []string{"object"},
			Properties:  props,
			Description: objdesc,
			Extensions:  ext,
			Examples:    examples,
		}
		if len(requiredFields) > 0 {
			schema.Required = requiredFields
		}
		return highbase.CreateSchemaProxy(schema), nil, nil
	} else {
		// use a reference to avoid circular references and endless loops
		typeId := d.Key()
		ref := typeId
		if scope == schemaScopeRequest && (model.HasRequestExceptions(t) || model.HasResponseExceptions(t)) {
			ref = ref + RequestExceptionTypeKeySuffix
		}
		if t, ok := s.typeMap[typeId]; ok {
			schemaComponentTypes[typeId] = t
		} else {
			return nil, nil, fmt.Errorf("schematize: %s: failed to find referenced type in typeMap: %s", ctx, ref)
		}

		var ext *orderedmap.Map[string, *yaml.Node]
		if pos, ok := t.Pos(); ok {
			filename := pos.Filename
			if relpath, err := filepath.Rel(s.basePath, filename); err != nil {
				return nil, nil, err
			} else {
				filename = relpath
			}
			ext = ext2(
				"x-oc-type-source-type", t.Key(),
				"x-oc-type-source-pos", fmt.Sprintf("%s:%d:%d", filename, pos.Line, pos.Column),
			)
		} else {
			ext = ext1("x-oc-type-source-type", t.Key())
		}

		return highbase.CreateSchemaProxyRef(SchemaComponentRefPrefix + ref), ext, nil
	}
}

func (s resolver) reqschema(param model.Param, im model.Impl, schemaComponentTypes map[string]model.Type, desc string) (*base.SchemaProxy, *orderedmap.Map[string, *yaml.Node], error) {
	if t, ok := s.typeMap[param.Name]; ok {
		return s.schematize(schemaScopeRequest, "reqschema", t, []string{im.Name}, schemaComponentTypes, desc)
	}

	var schemaRef *base.SchemaProxy = nil
	var ext *orderedmap.Map[string, *yaml.Node] = nil
	switch param.Name {
	case "map[string]any":
		schemaRef = base.CreateSchemaProxy(objectSchema(desc))
	case "string":
		schemaRef = base.CreateSchemaProxy(stringSchema(desc))
	case "[]string":
		schemaRef = base.CreateSchemaProxy(arraySchema(base.CreateSchemaProxy(stringSchema(desc))))
	case "any":
		schemaRef = base.CreateSchemaProxy(anySchema(desc))
	default:
		return nil, nil, fmt.Errorf("reqschema: failed to schematize unsupported response type '%s' for endpoint '%s %s' in function '%s'", param.Name, im.Endpoint.Verb, im.Endpoint.Path, im.Name)
		//schemaRef = base.CreateSchemaProxyRef(SchemaComponentRefPrefix + ref)
		//ext = ext1("x-oc-ref-source", "bodyparam of "+im.Name)
	}
	return schemaRef, ext, nil
}

func (s resolver) bodyparams(params []model.Param, im model.Impl, schemaComponentTypes map[string]model.Type) (*v3.RequestBody, error) {
	var schemaRef *highbase.SchemaProxy
	var err error
	desc := ""
	var examples *orderedmap.Map[string, *highbase.Example]
	switch len(params) {
	case 0:
		return nil, nil
	case 1:
		schemaRef, _, err = s.reqschema(params[0], im, schemaComponentTypes, params[0].Description)
		desc = params[0].Description
		if exampleSet, ok := s.m.Examples[params[0].Name]; ok {
			example, specific := exampleSet.ForRequest()
			if specific {
				rendered := s.renderedExampleMap[exampleSet.Key].forRequest()
				if rendered != nil {
					examples = omap1("ok", &highbase.Example{
						Summary:     "body",
						Description: example.Title,
						Value:       rendered,
						Extensions: ext2(
							"oc-example-source-file", example.Origin,
							"oc-example-scope", "bodyparam/"+string(schemaScopeRequest),
						),
					})
				}
			}
			if examples == nil {
				examples = omap1("ok", highbase.CreateExampleRef(ExampleComponentRefPrefix+example.Key))
			}

			if schemaRef.Schema() != nil {
				props := schemaRef.Schema().Properties
				if props != nil {
					for pair := props.Oldest(); pair != nil; pair = pair.Next() {
						if err := patchExampleIntoSchema(pair.Key, example, pair.Value.Schema()); err != nil {
							return nil, fmt.Errorf("bodyparams: failed to patch example into schema of '%s' property '%s': %w", params[0].Name, pair.Key, err)
						}
					}
				}
			}

		}
	default:
		schemaRef, err = mapReduce(params, func(ref model.Param) (*highbase.SchemaProxy, bool, error) {
			schemaRef, _, err := s.reqschema(ref, im, schemaComponentTypes, ref.Description)
			return schemaRef, true, err
		}, func(schemas []*highbase.SchemaProxy) (*highbase.SchemaProxy, error) {
			return base.CreateSchemaProxy(&base.Schema{OneOf: schemas}), nil
		})
		if err != nil {
			return nil, err
		}
		// TODO multiple body parameters, what should we use for the description and the examples?
	}

	return &v3.RequestBody{
		Required:    boolPtr(true),
		Content:     omap1("application/json", &v3.MediaType{Schema: schemaRef, Examples: examples}),
		Description: desc,
		Extensions:  ext1("x-oc-ref-source", "bodyparams of "+im.Name),
	}, nil
}

func specificResponseSummary(code int, s model.InferredSummary) string {
	switch code {
	case 404:
		if s.Child != "" && s.SpecificChild {
			// the specified {child} does not exist within that {obj}
			if s.ForAccount {
				return fmt.Sprintf("the account or the specified %s does not exist within that %s", s.Child, s.Object)
			} else {
				return fmt.Sprintf("the specified %s does not exist within that %s", s.Child, s.Object)
			}
		} else if s.Object != "" {
			// the specified {obj} does not exist
			if s.ForAccount {
				return fmt.Sprintf("the account or the specified %s does not exist", s.Object)
			} else {
				return fmt.Sprintf("the specified %s does not exist", s.Object)
			}
		}
	}
	return ""
}

func (s resolver) responses(im model.Impl, m model.Model, schemaComponentTypes map[string]model.Type) (*v3.Responses, error) {
	respMap := orderedmap.New[string, *v3.Response]()

	resps := map[int]model.Resp{}
	maps.Copy(resps, im.Resp)
	for _, code := range []int{404} {
		if _, ok := resps[code]; !ok {
			// there is no specific 404, but we might be able to generate a summary that is more specific than
			// the generic default 404 one and, if so, we should inject that response into resps to process it below,
			// as a function specific response, and not as a generic error response reference (with the generic
			// description)
			if summary := specificResponseSummary(code, im.InferredSummary); summary != "" {
				var respType model.Type = nil
				if t, ok := s.typeMap["groupware.ErrorResponse"]; ok {
					respType = t
				}
				resps[code] = model.Resp{
					Type:    respType,
					Summary: summary,
				}
			}
		}
	}

	for code, resp := range resps {
		contentMap := orderedmap.New[string, *v3.MediaType]()
		if resp.Type != nil {
			if schema, ext, err := s.schematize(
				schemaScopeResponse,
				fmt.Sprintf("verb='%s' path='%s' fun='%s': response type '%s'", im.Endpoint.Verb, im.Endpoint.Path, im.Endpoint.Fun, resp.Type.Key()),
				resp.Type, []string{resp.Type.Name()}, schemaComponentTypes, ""); err != nil {
				return nil, fmt.Errorf("failed to reference response type %s: %v", resp.Type, err)
			} else {
				var examples *orderedmap.Map[string, *highbase.Example]
				if _, ok := m.Examples[resp.Type.Key()]; ok {
					examples = omap1("ok", highbase.CreateExampleRef(ExampleComponentRefPrefix+resp.Type.Key()))
				}
				contentMap.Set("application/json", &v3.MediaType{
					Schema:     schema,
					Extensions: ext,
					Examples:   examples,
				})
			}
		} else {
			// when Type is nil, it means that there is no response object, used with 204 No Content,
			// but we still have to add the Response object for that code below
		}

		summary := resp.Summary
		if summary == "" {
			obj := im.InferredSummary.Object
			spec := im.InferredSummary.SpecificObject
			if im.InferredSummary.Child != "" {
				obj = im.InferredSummary.Child
				spec = im.InferredSummary.SpecificChild
			}
			if obj != "" && im.InferredSummary.Action != "" && code >= 200 && code < 300 {
				switch im.InferredSummary.Action {
				case "retrieve":
					if (im.InferredSummary.ForAllAccounts || im.InferredSummary.ForAccount) && im.InferredSummary.Child == "" {
						if spec {
							if im.InferredSummary.ForAllAccounts {
								// the email corresponding to the specified identifier, across all accounts
								summary = fmt.Sprintf("the %s corresponding to the specified identifier, across all accounts", obj)
							} else {
								// the email corresponding to the specified identifier, for that account
								summary = fmt.Sprintf("the %s corresponding to the specified identifier, for that account", obj)
							}
						} else {
							if im.InferredSummary.ForAllAccounts {
								// the email for all accounts
								summary = fmt.Sprintf("the %s for all accounts", obj)
							} else {
								// the email for the specified account
								summary = fmt.Sprintf("the %s for the specified account", obj)
							}
						}
					} else {
						// retrieve => the successfully retrieved obj
						summary = fmt.Sprintf("the successfully %s %s", im.InferredSummary.Adjective, obj)
					}
				case "create", "replace", "modify":
					// create => the successfully created obj
					// replace => the successfully replaced obj
					// modify => the successfully modified obj
					summary = fmt.Sprintf("the successfully %s %s", im.InferredSummary.Adjective, obj)
				case "delete":
					// delete => the obj was deleted successfully
					summary = fmt.Sprintf("the %s was %s successfully", obj, im.InferredSummary.Adjective)
				default:
					return nil, fmt.Errorf("unsupported inferred summary action '%s'", im.InferredSummary.Action)
				}
			}
		}
		if summary == "" {
			summary = specificResponseSummary(code, im.InferredSummary)
		}
		if summary == "" {
			// as a very last resort
			summary = http.StatusText(code)
		}

		// common response headers
		headers := orderedmap.New[string, *v3.Header]()
		for k, h := range m.DefaultResponseHeaders {
			if !h.IsApplicable(code) {
				continue
			}
			headers.Set(k, v3.CreateHeaderRef(HeaderComponentRefPrefix+k))
		}

		respMap.Set(strconv.Itoa(code), &v3.Response{
			// Summary: // not displayed, use Description
			Description: summary,
			Content:     contentMap,
			Extensions:  ext1("x-of-source", fmt.Sprintf("responses of %s at %s:%d%d", im.Name, im.Filename, im.Line, im.Column)),
			Headers:     headers,
		})
	}
	// also add the default responses in every operation
	for code, respType := range m.DefaultResponses {
		codeKey := strconv.Itoa(code)
		if _, ok := respMap.Get(codeKey); ok {
			// that code is already defined for that function, which overrides the generic default
			// response for that code, so don't do anything here
		} else if respType != nil {
			ref := fmt.Sprintf("%s.%d", respType.Key(), code)
			respMap.Set(codeKey, &v3.Response{
				Reference: ResponseComponentRefPrefix + ref,
				Summary:   http.StatusText(code),
			})
		} else {
			// no type means that there is no response, e.g. for 204 No Content
			respMap.Set(codeKey, &v3.Response{
				Summary: http.StatusText(code),
			})
		}
	}
	return &v3.Responses{Codes: respMap}, nil
}
