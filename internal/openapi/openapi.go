package openapi

import (
	"fmt"
	"io"
	"log"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	oapi "github.com/pb33f/libopenapi"
	base "github.com/pb33f/libopenapi/datamodel/high/base"
	highbase "github.com/pb33f/libopenapi/datamodel/high/base"
	v3 "github.com/pb33f/libopenapi/datamodel/high/v3"
	"github.com/pb33f/libopenapi/orderedmap"
	"go.yaml.in/yaml/v4"
	"opencloud.eu/groupware-apidocs/internal/model"
)

var (
	openIdConnectUrl = "https://keycloak.opencloud.test/realms/openCloud/.well-known/openid-configuration"
	contact          = &base.Contact{
		Name:  "Pascal Bleser",
		Email: "p.bleser@opencloud.eu",
	}
)

type OpenApiSink struct {
	BasePath         string
	TemplateFile     string
	IncludeBasicAuth bool
}

var _ model.Sink = OpenApiSink{}

func (s OpenApiSink) newPathItem(_ string, _ string, _ model.Impl) *v3.PathItem {
	return &v3.PathItem{}
}

func (s OpenApiSink) newOperation(id string, _ string, _ string, im model.Impl) *v3.Operation {
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
	//anySchema    = &base.Schema{Type: []string{"object"}}
	//objectSchema = &base.Schema{Type: []string{"object"}}
	//stringSchema          = &base.Schema{Type: []string{"string"}}
	//integerSchema         = &base.Schema{Type: []string{"integer"}}
	//unsignedIntegerSchema = &base.Schema{Type: []string{"integer"}, Minimum: zerofPtr()}
	//booleanSchema         = &base.Schema{Type: []string{"boolean"}}
	//stringArraySchema     = makeArraySchema(base.CreateSchemaProxy(stringSchema))
	//timeSchema            = &base.Schema{Type: []string{"string"}, Format: "date-time"}
	patchObjectSchema = arraySchema(base.CreateSchemaProxy(anySchema("")))
	// numberSchema = &base.Schema{Type: []string{"number"}}
	// dateSchema = &base.Schema{Type: []string{"string"}, Format: "date"}
)

func timeSchema(d string) *highbase.Schema {
	return &base.Schema{Type: []string{"string"}, Format: "date-time", Description: d}
}

func anySchema(d string) *highbase.Schema {
	return &base.Schema{Type: []string{"object"}, Description: d}
}

func objectSchema(d string) *highbase.Schema {
	return &base.Schema{Type: []string{"object"}, Description: d}
}

func stringSchema(d string) *highbase.Schema {
	return &base.Schema{Type: []string{"string"}, Description: d}
}

func integerSchema(d string) *highbase.Schema {
	return &base.Schema{Type: []string{"integer"}, Description: d}
}

func unsignedIntegerSchema(d string) *highbase.Schema {
	return &base.Schema{Type: []string{"integer"}, Minimum: zerofPtr(), Description: d}
}

func booleanSchema(d string) *highbase.Schema {
	return &base.Schema{Type: []string{"boolean"}, Description: d}
}

func (s OpenApiSink) parameterize(p model.Param, in string, requiredByDefault bool, m map[string]model.Param,
	typeMap map[string]model.Type,
	schemaComponentTypes map[string]model.Type) (*v3.Parameter, error) {
	if g, ok := m[p.Name]; ok {
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
		var schema *highbase.SchemaProxy
		var ext *orderedmap.Map[string, *yaml.Node]
		if p.Type != nil {
			if s, e, err := s.schematize(fmt.Sprintf("parameterize(%v, %s)", p, in), p.Type, []string{p.Name}, typeMap, schemaComponentTypes, desc); err != nil {
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

func (s OpenApiSink) Output(m model.Model, w io.Writer) error {
	var template *v3.Document = nil
	{
		if s.TemplateFile != "" {
			if f, err := os.ReadFile(s.TemplateFile); err != nil {
				return fmt.Errorf("failed to load template '%s': %v", s.TemplateFile, err)
			} else {
				if d, err := oapi.NewDocument(f); err != nil {
					return fmt.Errorf("failed to create document from template '%s': %v", s.TemplateFile, err)
				} else {
					if m, err := d.BuildV3Model(); err != nil {
						return fmt.Errorf("failed to build v3 model from template '%s': %v", s.TemplateFile, err)
					} else {
						template = &m.Model
					}
				}
			}
		}
	}

	typeMap := index(m.Types, func(t model.Type) string { return t.Key() })
	imMap := index(m.Impls, func(i model.Impl) string { return i.Name })
	paths := indexMany(m.Routes, func(r model.Endpoint) string { return r.Path })

	pathItemMap := orderedmap.New[string, *v3.PathItem]()
	schemaComponentTypes := map[string]model.Type{} // collects items that need to be documented in /components/schemas

	pathKeys := slices.Collect(maps.Keys(paths))
	slices.Sort(pathKeys)

	for _, path := range pathKeys {
		var pathItem *v3.PathItem = nil
		{
			opByVerb := map[string]*v3.Operation{}

			for _, r := range paths[path] {
				var op *v3.Operation = nil
				{
					im, ok := imMap[r.Fun]
					if !ok {
						return fmt.Errorf("verb='%s' path='%s' fun='%s': failed to find function in imMap for '%s'", r.Verb, r.Path, r.Fun, r.Fun)
					}

					if pathItem == nil {
						pathItem = s.newPathItem(r.Verb, path, im)
					}

					opid := r.Fun
					op = s.newOperation(opid, r.Verb, path, im)

					// path parameters
					for _, p := range im.PathParams {
						if param, err := s.parameterize(p, "path", true, m.PathParams, typeMap, schemaComponentTypes); err != nil {
							return fmt.Errorf("verb='%s' path='%s' fun='%s': %s", r.Verb, r.Path, r.Fun, err)
						} else {
							op.Parameters = append(op.Parameters, param)
						}
					}

					// query parameters
					for _, p := range im.QueryParams {
						if param, err := s.parameterize(p, "query", false, m.QueryParams, typeMap, schemaComponentTypes); err != nil {
							return fmt.Errorf("verb='%s' path='%s' fun='%s': %s", r.Verb, r.Path, r.Fun, err)
						} else {
							op.Parameters = append(op.Parameters, param)
						}
					}

					// header parameters
					for _, p := range im.HeaderParams {
						if param, err := s.parameterize(p, "header", false, m.HeaderParams, typeMap, schemaComponentTypes); err != nil {
							return fmt.Errorf("verb='%s' path='%s' fun='%s': %s", r.Verb, r.Path, r.Fun, err)
						} else {
							op.Parameters = append(op.Parameters, param)
						}
					}

					// common header parameters
					for _, h := range m.CommonRequestHeaders {
						req := false
						if h.Required {
							req = true
						}
						schemaRef := highbase.CreateSchemaProxy(stringSchema(h.Description + " (x)"))
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
					if responses, err := s.responses(im, m, typeMap, schemaComponentTypes); err != nil {
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
				}

				if op != nil {
					if _, ok := opByVerb[r.Verb]; ok {
						return fmt.Errorf("conflict: path '%s' already has an operation assigned to the verb '%s'", r.Path, r.Verb)
					} else {
						opByVerb[r.Verb] = op
					}
				}
			}

			opVerbs := slices.Collect(maps.Keys(opByVerb))
			slices.SortFunc(opVerbs, verbSort)
			for _, verb := range opVerbs {
				if op, ok := opByVerb[verb]; ok {
					if err := assign(op, verb, pathItem); err != nil {
						return err
					}
				}
			}
		}
		if pathItem != nil {
			pathItemMap.Set(path, pathItem)
		}
	}

	componentResponses := orderedmap.New[string, *v3.Response]()
	{
		for code, t := range m.DefaultResponses {
			if t == nil {
				continue
			}
			key := fmt.Sprintf("%s.%d", t.Key(), code)
			contentMap := orderedmap.New[string, *v3.MediaType]()
			if schema, ext, err := s.schematize(fmt.Sprintf("default response '%s'", t.Name()), t, []string{t.Name()}, typeMap, schemaComponentTypes, ""); err != nil {
				return fmt.Errorf("failed to reference default response type %s: %v", t, err)
			} else {
				contentMap.Set("application/json", &v3.MediaType{
					Schema:     schema,
					Extensions: ext,
				})
			}

			// TODO extract default response summary and description from comments
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
		m := map[string]*highbase.SchemaProxy{}

		// since each type may reference other types (typically struct fields), we have to loop
		// indefinitely until we don't have any additional types to document that haven't been
		// documented yet
		{
			maxIterations := 20 // just to avoid an infinite loop
			i := 0
			for ; i < maxIterations && len(schemaComponentTypes) > 0; i++ {
				// keep track of other types that are referenced within those types
				moreSchemaComponentTypes := map[string]model.Type{}
				for _, t := range schemaComponentTypes {
					if strings.HasPrefix(t.Name(), "Swagger") {
						continue
					}
					ctx := fmt.Sprintf("resolving schema component type '%s'", t.Key())
					if schema, _, err := s.schematize(ctx, t, []string{}, typeMap, moreSchemaComponentTypes, t.Summary()); err == nil {
						if schema != nil {
							m[t.Key()] = schema
						}
					} else {
						return err
					}
				}

				{
					x := map[string]model.Type{}
					for k, v := range moreSchemaComponentTypes {
						if _, ok := m[k]; ok {
							// we've already processed this type
						} else {
							// haven't done that one yet, keep it in the to-do list
							x[k] = v
						}
					}
					moreSchemaComponentTypes = x
				}

				{
					before := slices.Collect(maps.Keys(schemaComponentTypes))
					slices.Sort(before)
					after := slices.Collect(maps.Keys(moreSchemaComponentTypes))
					slices.Sort(after)
					if slices.Equal(before, after) {
						log.Panicf("round %d: failure to resolve the following schema component types, remaining unresolved after an iteration: %s", i, strings.Join(before, ", "))
					}
				}
				schemaComponentTypes = moreSchemaComponentTypes
			}
			if i >= maxIterations {
				log.Panicf("documenting the schemas of types has iterated %d times, which is more than the limit of %d", i, maxIterations)
			}
		}

		// sort them by keys to have a predictable output
		mkeys := slices.Collect(maps.Keys(m))
		slices.Sort(mkeys)
		for _, k := range mkeys {
			if v, ok := m[k]; ok {
				componentSchemas.Set(k, v)
			}
		}
	}

	componentHeaders := orderedmap.New[string, *v3.Header]()
	{
		for name, desc := range m.DefaultResponseHeaders {
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
				Schema:      highbase.CreateSchemaProxy(stringSchema(desc.Summary)),
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
		if s.IncludeBasicAuth {
			securitySchemes.Set("basic", &v3.SecurityScheme{
				Type:        "http",
				Scheme:      "basic",
				Description: "Basic Authentication for API Calls, if enabled",
			})
		}
	}
	components.SecuritySchemes = securitySchemes

	extensions := orderedmap.New[string, *yaml.Node]()
	if len(m.UndocumentedResults) > 0 || len(m.UndocumentedRequestBodies) > 0 {
		container := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		if len(m.UndocumentedResults) > 0 {
			parent := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			{
				k := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "results"}
				container.Content = append(container.Content, k, parent)
			}

			for id, u := range m.UndocumentedResults {
				filename := u.Pos.Filename
				if relpath, err := filepath.Rel(s.BasePath, filename); err != nil {
					return err
				} else {
					filename = relpath
				}

				c := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
				{
					k := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: id}
					parent.Content = append(parent.Content, k, c)
				}

				{
					k := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "pos"}
					v := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: fmt.Sprintf("%s:%d:%d", filename, u.Pos.Line, u.Pos.Column)}
					c.Content = append(c.Content, k, v)
				}
				{
					k := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "verb"}
					v := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: u.Endpoint.Verb}
					c.Content = append(c.Content, k, v)
				}
				{
					k := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "path"}
					v := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: u.Endpoint.Path}
					c.Content = append(c.Content, k, v)
				}
			}
		}

		if len(m.UndocumentedRequestBodies) > 0 {
			parent := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			{
				k := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "bodies"}
				container.Content = append(container.Content, k, parent)
			}

			for id, u := range m.UndocumentedRequestBodies {
				filename := u.Pos.Filename
				if relpath, err := filepath.Rel(s.BasePath, filename); err != nil {
					return err
				} else {
					filename = relpath
				}

				c := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
				{
					k := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: id}
					parent.Content = append(parent.Content, k, c)
				}

				{
					k := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "pos"}
					v := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: fmt.Sprintf("%s:%d:%d", filename, u.Pos.Line, u.Pos.Column)}
					c.Content = append(c.Content, k, v)
				}
				{
					k := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "verb"}
					v := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: u.Endpoint.Verb}
					c.Content = append(c.Content, k, v)
				}
				{
					k := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "path"}
					v := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: u.Endpoint.Path}
					c.Content = append(c.Content, k, v)
				}
			}
		}

		extensions.Set("x-oc-undocumented", container)
	}

	doc := &v3.Document{
		Version: "3.0.4",
		Info: &base.Info{
			Title:   "OpenCloud Groupware API",
			Contact: contact,
			Version: "0.0.0",
		},
		Paths: &v3.Paths{
			PathItems: pathItemMap,
		},
		Components: components,
		Extensions: extensions,
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

	if template != nil {
		if template.Servers != nil {
			if len(doc.Servers) > 0 {
				return fmt.Errorf("can't merge servers from template: document has servers of its own")
			}
			doc.Servers = template.Servers // merging not implemented
		}
		merge(&doc.Extensions, template.Extensions)
		if template.Tags != nil {
			for _, tag := range template.Tags {
				if d, ok := find(doc.Tags, func(t *highbase.Tag) bool { return t.Name == tag.Name }); ok {
					if d.Summary == "" {
						d.Summary = tag.Summary
					}
					if d.Description == "" {
						d.Description = tag.Description
					}
					merge(&d.Extensions, tag.Extensions)
				} else {
					doc.Tags = append(doc.Tags, tag)
				}
			}
		}
	}

	// logging output to stderr
	if len(m.UndocumentedResults) > 0 {
		log.Printf("%d undocumented results:", len(m.UndocumentedResults))
		for name, u := range m.UndocumentedResults {
			filename := u.Pos.Filename
			if relpath, err := filepath.Rel(s.BasePath, filename); err != nil {
				return err
			} else {
				filename = relpath
			}
			log.Printf("  - %s: %s %s @ %s:%d:%d", name, u.Endpoint.Verb, u.Endpoint.Path, filename, u.Pos.Line, u.Pos.Column)
		}
	}
	if len(m.UndocumentedRequestBodies) > 0 {
		log.Printf("%d undocumented request bodies:", len(m.UndocumentedRequestBodies))
		for name, u := range m.UndocumentedRequestBodies {
			filename := u.Pos.Filename
			if relpath, err := filepath.Rel(s.BasePath, filename); err != nil {
				return err
			} else {
				filename = relpath
			}
			log.Printf("  - %s: %s %s @ %s:%d:%d", name, u.Endpoint.Verb, u.Endpoint.Path, filename, u.Pos.Line, u.Pos.Column)
		}
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

func (s OpenApiSink) schematize(ctx string, t model.Type, path []string, typeMap map[string]model.Type, schemaComponentTypes map[string]model.Type, desc string) (*highbase.SchemaProxy, *orderedmap.Map[string, *yaml.Node], error) {
	if t == nil {
		return nil, nil, fmt.Errorf("schematize: t is nil (path=%s)", strings.Join(path, " > "))
	}
	if elt, ok := t.Element(); ok {
		if t.IsMap() {
			if deref, ext, err := s.schematize(ctx, elt, sappend(path, t.Name()), typeMap, schemaComponentTypes, desc); err != nil {
				return nil, nil, err
			} else {
				schema := makeObjectSchema(deref)
				schema.Extensions = ext
				schema.Description = desc
				return highbase.CreateSchemaProxy(schema), nil, nil
			}
		} else if t.IsArray() {
			if deref, ext, err := s.schematize(ctx, elt, sappend(path, t.Name()), typeMap, schemaComponentTypes, desc); err != nil {
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
		props := orderedmap.New[string, *highbase.SchemaProxy]()
		for _, f := range d.Fields() {
			ctx := fmt.Sprintf("%s.%s", ctx, f.Attr)
			if fs, _, err := s.schematize(ctx, f.Type, sappend(path, t.Name()), typeMap, schemaComponentTypes, f.Summary); err != nil {
				return nil, nil, err
			} else if fs != nil {
				if f.Attr == "" {
					return nil, nil, fmt.Errorf("struct property in '%s' has no attr value: %#v", t.Key(), f)
				}
				props.Set(f.Attr, fs)
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

		var ext *orderedmap.Map[string, *yaml.Node]
		if pos, ok := t.Pos(); ok {
			filename := pos.Filename
			if relpath, err := filepath.Rel(s.BasePath, filename); err != nil {
				return nil, nil, err
			} else {
				filename = relpath
			}
			ext = ext2("x-oc-type-source-struct", t.Key(), "x-oc-type-source-pos", fmt.Sprintf("%s:%d:%d", filename, pos.Line, pos.Column))
		} else {
			ext = ext1("x-oc-type-source-struct", t.Key())
		}

		schema := &highbase.Schema{
			Type:        []string{"object"},
			Properties:  props,
			Title:       strings.Join(path, " > "), // for debugging
			Description: objdesc,
			Extensions:  ext,
		}

		return highbase.CreateSchemaProxy(schema), nil, nil
	} else {
		// use a reference to avoid circular references and endless loops
		ref := d.Key()
		if t, ok := typeMap[ref]; ok {
			schemaComponentTypes[ref] = t
		} else {
			return nil, nil, fmt.Errorf("schematize: failed to find referenced type in typeMap: %s", ref)
		}

		var ext *orderedmap.Map[string, *yaml.Node]
		if pos, ok := t.Pos(); ok {
			filename := pos.Filename
			if relpath, err := filepath.Rel(s.BasePath, filename); err != nil {
				return nil, nil, err
			} else {
				filename = relpath
			}
			ext = ext2("x-oc-type-source-ref", t.Key(), "x-oc-type-source-pos", fmt.Sprintf("%s:%d:%d", filename, pos.Line, pos.Column))
		} else {
			ext = ext1("x-oc-type-source-ref", t.Key())
		}

		return highbase.CreateSchemaProxyRef(SchemaComponentRefPrefix + ref), ext, nil
	}
}

func (s OpenApiSink) reqschema(param model.Param, im model.Impl, typeMap map[string]model.Type, schemaComponentTypes map[string]model.Type, desc string) (*base.SchemaProxy, *orderedmap.Map[string, *yaml.Node], error) {
	if t, ok := typeMap[param.Name]; ok {
		return s.schematize("reqschema", t, []string{im.Name}, typeMap, schemaComponentTypes, desc)
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

func (s OpenApiSink) bodyparams(params []model.Param, im model.Impl, typeMap map[string]model.Type, schemaComponentTypes map[string]model.Type) (*v3.RequestBody, error) {
	var schemaRef *highbase.SchemaProxy
	var err error
	desc := ""
	switch len(params) {
	case 0:
		return nil, nil
	case 1:
		schemaRef, _, err = s.reqschema(params[0], im, typeMap, schemaComponentTypes, params[0].Description)
		desc = params[0].Description
	default:
		schemaRef, err = mapReduce(params, func(ref model.Param) (*highbase.SchemaProxy, bool, error) {
			schemaRef, _, err := s.reqschema(ref, im, typeMap, schemaComponentTypes, ref.Description)
			return schemaRef, true, err
		}, func(schemas []*highbase.SchemaProxy) (*highbase.SchemaProxy, error) {
			return base.CreateSchemaProxy(&base.Schema{OneOf: schemas}), nil
		})
		if err != nil {
			return nil, err
		}
		// TODO multiple body parameters, what should we use for the description?
	}
	return &v3.RequestBody{
		Required:    boolPtr(true),
		Content:     omap1("application/json", &v3.MediaType{Schema: schemaRef}),
		Description: desc,
		Extensions:  ext1("x-oc-ref-source", "bodyparams of "+im.Name),
	}, nil
}

func (s OpenApiSink) responses(im model.Impl, model model.Model, typeMap map[string]model.Type, schemaComponentTypes map[string]model.Type) (*v3.Responses, error) {
	respMap := orderedmap.New[string, *v3.Response]()
	for code, resp := range im.Resp {
		contentMap := orderedmap.New[string, *v3.MediaType]()
		if resp.Type != nil {
			if schema, ext, err := s.schematize(
				fmt.Sprintf("verb='%s' path='%s' fun='%s': response type '%s'", im.Endpoint.Verb, im.Endpoint.Path, im.Endpoint.Fun, resp.Type.Key()),
				resp.Type, []string{resp.Type.Name()}, typeMap, schemaComponentTypes, ""); err != nil {
				return nil, fmt.Errorf("failed to reference response type %s: %v", resp.Type, err)
			} else {
				contentMap.Set("application/json", &v3.MediaType{
					Schema:     schema,
					Extensions: ext,
				})
			}
		} else {
			// when Type is nil, it means that there is no response object, used with 204 No Content,
			// but we still have to add the Response object for that code below
		}

		// TODO extract response summary from apidoc annotation
		summary := ""
		if resp.Type != nil {
			summary = resp.Type.Summary()
		}
		if summary == "" {
			summary = http.StatusText(code)
		}

		respMap.Set(strconv.Itoa(code), &v3.Response{
			Description: summary,
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

func assign(op *v3.Operation, verb string, pathItem *v3.PathItem) error {
	switch verb {
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
		pathItem.AdditionalOperations.Set(verb, op)
	}
	return nil
}

func arraySchema(itemSchema *highbase.SchemaProxy) *highbase.Schema {
	return &base.Schema{
		Type:  []string{"array"},
		Items: &base.DynamicValue[*base.SchemaProxy, bool]{N: 0, A: itemSchema},
	}
}

func makeObjectSchema(itemSchema *highbase.SchemaProxy) *highbase.Schema {
	return &base.Schema{
		Type:                 []string{"object"},
		AdditionalProperties: &base.DynamicValue[*base.SchemaProxy, bool]{N: 0, A: itemSchema},
	}
}

func ext1(k string, v string) *orderedmap.Map[string, *yaml.Node] {
	ext := orderedmap.New[string, *yaml.Node]()
	ext.Set(k, &yaml.Node{Kind: yaml.ScalarNode, Value: v})
	return ext
}

func ext2(k1, v1, k2, v2 string) *orderedmap.Map[string, *yaml.Node] {
	ext := orderedmap.New[string, *yaml.Node]()
	ext.Set(k1, &yaml.Node{Kind: yaml.ScalarNode, Value: v1})
	ext.Set(k2, &yaml.Node{Kind: yaml.ScalarNode, Value: v2})
	return ext
}

func boolPtr(b bool) *bool {
	return &b
}

func zerofPtr() *float64 {
	var f float64 = 0
	return &f
}

func sappend[E any](s []E, elem E) []E {
	c := make([]E, len(s)+1)
	copy(c, s)
	c[len(s)] = elem
	return c
}

func index[K comparable, V any](s []V, indexer func(V) K) map[K]V {
	m := map[K]V{}
	for _, v := range s {
		k := indexer(v)
		m[k] = v
	}
	return m
}

func indexMany[K comparable, V any](s []V, indexer func(V) K) map[K][]V {
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

func omap1[K comparable, V any](k K, v V) *orderedmap.Map[K, V] {
	m := orderedmap.New[K, V]()
	m.Set(k, v)
	return m
}

func mapReduce[A any, B any, C any](s []A, mapper func(A) (B, bool, error), reducer func([]B) (C, error)) (C, error) {
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
