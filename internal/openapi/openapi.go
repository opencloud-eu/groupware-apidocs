package openapi

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	oapi "github.com/pb33f/libopenapi"
	base "github.com/pb33f/libopenapi/datamodel/high/base"
	highbase "github.com/pb33f/libopenapi/datamodel/high/base"
	v3 "github.com/pb33f/libopenapi/datamodel/high/v3"
	"github.com/pb33f/libopenapi/orderedmap"
	"go.yaml.in/yaml/v4"
	"opencloud.eu/groupware-apidocs/internal/model"
	"opencloud.eu/groupware-apidocs/internal/tools"
)

var (
	openIdConnectUrl         = "https://keycloak.opencloud.test/realms/openCloud/.well-known/openid-configuration"
	schemaPropertiesExamples = false // can be toggled, but property examples in schemas are not rendered in redoc, so we should leave this on false
)

type OpenApiSink struct {
	BasePath         string
	TemplateFile     string
	IncludeBasicAuth bool
}

func NewOpenApiSink(basepath string, templateFile string, includeBasicAuth bool) OpenApiSink {
	return OpenApiSink{
		BasePath:         basepath,
		TemplateFile:     templateFile,
		IncludeBasicAuth: includeBasicAuth,
	}
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
	ExampleComponentRefPrefix  = "#/components/examples/"
	HeaderComponentRefPrefix   = "#/components/headers/"

	RequestExceptionTypeKeySuffix = ".forRequests"
)

var (
	TagUnified = "unified"
)

var (
	patchObjectSchema = arraySchema(base.CreateSchemaProxy(anySchema("")))
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
	return &base.Schema{Type: []string{"integer"}, Minimum: tools.ZerofPtr(), Description: d}
}

func booleanSchema(d string) *highbase.Schema {
	return &base.Schema{Type: []string{"boolean"}, Description: d}
}

type renderedExample struct {
	defaultExample *yaml.Node
	requestExample *yaml.Node
}

func (r renderedExample) forRequest() *yaml.Node {
	if r.requestExample != nil {
		return r.requestExample
	}
	return r.defaultExample
}

func (r renderedExample) forResponse() *yaml.Node {
	return r.defaultExample
}

func renderExamples(qualified model.Examples, patcher func(*yaml.Node) *yaml.Node) map[string]renderedExample {
	defaultExampleByKey := map[string]*yaml.Node{}
	requestExampleByKey := map[string]*yaml.Node{}
	for _, e := range qualified.DefaultExamples {
		if n, err := renderExample(e.Text); err != nil {
			panic(fmt.Errorf("failed to render default example for '%s': %w", e.Key, err))
		} else {
			if n.Kind == yaml.DocumentNode {
				patched := []*yaml.Node{}
				for _, c := range n.Content {
					patched = append(patched, patcher(c))
				}
				n.Content = patched
			} else {
				n = patcher(n)
			}
			defaultExampleByKey[e.Key] = n
		}
	}
	for _, e := range qualified.RequestExamples {
		if n, err := renderExample(e.Text); err != nil {
			panic(fmt.Errorf("failed to render request example for '%s': %w", e.Key, err))
		} else {
			n = patcher(n)
			requestExampleByKey[e.Key] = n
		}
	}

	keys := tools.UniqKeys(defaultExampleByKey, requestExampleByKey)
	result := map[string]renderedExample{}
	for _, k := range keys {
		rendered := renderedExample{}
		if defaultExample, ok := defaultExampleByKey[k]; ok {
			rendered.defaultExample = defaultExample
			if requestExample, ok := requestExampleByKey[k]; ok {
				rendered.requestExample = requestExample
			}
		} else {
			panic(fmt.Errorf("no default example for '%s'", k))
		}
		result[k] = rendered
	}
	return result
}

func renderExamplesAsArrays(qualified model.Examples) map[string]renderedExample {
	return renderExamples(qualified, func(s *yaml.Node) *yaml.Node {
		ary := yaml.Node{
			Kind: yaml.SequenceNode,
		}
		ary.Content = []*yaml.Node{s}
		return &ary
	})
}

func renderExamplesAsMaps(key string, qualified model.Examples) map[string]renderedExample {
	return renderExamples(qualified, func(s *yaml.Node) *yaml.Node {
		dict := yaml.Node{
			Kind: yaml.MappingNode,
			Tag:  "!!map",
		}
		keyNode := yaml.Node{
			Kind:  yaml.ScalarNode,
			Tag:   "!!str",
			Value: key,
		}
		dict.Content = []*yaml.Node{&keyNode, s}
		return &dict
	})
}

func renderExample(text string) (*yaml.Node, error) {
	var data yaml.Node
	{
		var m any
		if err := json.Unmarshal([]byte(text), &m); err != nil {
			return nil, err
		}
		if b, err := yaml.Marshal(m); err != nil {
			return nil, err
		} else {
			if err := yaml.Unmarshal(b, &data); err != nil {
				return nil, err
			}
		}
	}
	return &data, nil
}

func renderObj(value any) (*yaml.Node, error) {
	ser := yaml.Node{}
	if b, err := yaml.Marshal(value); err != nil {
		return nil, err
	} else {
		if err := yaml.Unmarshal(b, &ser); err != nil {
			return nil, err
		}
	}
	if len(ser.Content) == 1 {
		return ser.Content[0], nil
	} else {
		return &ser, nil
	}
}

func patchExampleIntoSchema(name string, examples []model.Example, schema *highbase.Schema) error {
	if !schemaPropertiesExamples {
		return nil
	}
	if len(examples) < 1 {
		return nil
	}
	t := map[string]any{}
	for _, example := range examples {
		if err := json.Unmarshal([]byte(example.Text), &t); err != nil {
			return fmt.Errorf("bodyparams: failed to unmarshall example JSON payload: %w", err)
		} else {
			if value, ok := t[name]; ok {
				examples := schema.Examples
				if examples == nil {
					examples = []*yaml.Node{}
				}
				if ser, err := renderObj(value); err != nil {
					return fmt.Errorf("bodyparams: failed to marshall example JSON payload for attribute: %w", err)
				} else {
					examples = append(examples, ser)
				}
				schema.Examples = examples
			}
		}
	}
	return nil
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

	imMap := tools.Index(m.Impls, func(i model.Impl) string { return i.Name })
	routesByPath := tools.IndexMany(m.Routes, func(r model.Endpoint) string { return r.Path })
	renderedExampleMap := map[string]renderedExample{}
	for _, qualified := range m.Examples {
		maps.Copy(renderedExampleMap, renderExamples(qualified, tools.Identity))
	}

	pathItemMap := orderedmap.New[string, *v3.PathItem]()
	schemaComponentTypes := map[string]model.Type{} // collects items that need to be documented in /components/schemas
	neededArrayExamples := map[string]bool{}
	neededMapExamples := map[string]bool{}
	responsesWithoutExamples := map[string]bool{}

	pathKeys := slices.Collect(maps.Keys(routesByPath))
	slices.Sort(pathKeys)

	res := newResolver(s.BasePath, m, renderedExampleMap)

	for _, path := range pathKeys {
		var pathItem *v3.PathItem = nil
		{
			opByVerb := map[string]*v3.Operation{}

			for _, r := range routesByPath[path] {
				var op *v3.Operation = nil
				{
					im, ok := imMap[r.Fun]
					if !ok {
						return fmt.Errorf("verb='%s' path='%s' fun='%s': failed to find function in imMap for '%s'", r.Verb, r.Path, r.Fun, r.Fun)
					}

					if pathItem == nil {
						pathItem = s.newPathItem(r.Verb, r.Path, im)
					}

					opid := r.Fun
					op = s.newOperation(opid, r.Verb, r.Path, im)

					// path parameters
					for _, p := range im.PathParams {
						if param, err := res.parameter(p, "path", true, m.PathParams, schemaComponentTypes); err != nil {
							return fmt.Errorf("verb='%s' path='%s' fun='%s': %s", r.Verb, r.Path, r.Fun, err)
						} else {
							op.Parameters = append(op.Parameters, param)
						}
					}

					// query parameters
					for _, p := range im.QueryParams {
						if param, err := res.parameter(p, "query", false, m.QueryParams, schemaComponentTypes); err != nil {
							return fmt.Errorf("verb='%s' path='%s' fun='%s': %s", r.Verb, r.Path, r.Fun, err)
						} else {
							op.Parameters = append(op.Parameters, param)
						}
					}

					// header parameters
					for _, p := range im.HeaderParams {
						if param, err := res.parameter(p, "header", false, m.HeaderParams, schemaComponentTypes); err != nil {
							return fmt.Errorf("verb='%s' path='%s' fun='%s': %s", r.Verb, r.Path, r.Fun, err)
						} else {
							op.Parameters = append(op.Parameters, param)
						}
					}

					// common header parameters
					for _, h := range m.CommonRequestHeaders {
						schemaRef := highbase.CreateSchemaProxy(stringSchema(h.Description))
						param := &v3.Parameter{
							Name:        h.Name,
							In:          "header",
							Schema:      schemaRef,
							Description: h.Description,
							Required:    &h.Required,
							Explode:     &h.Exploded,
						}
						if len(h.Examples) > 0 {
							examples := orderedmap.New[string, *highbase.Example]()
							for summary, e := range h.Examples {
								examples.Set(summary, &highbase.Example{
									Summary: summary,
									Value:   &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: e},
								})
							}
							param.Examples = examples
						}
						op.Parameters = append(op.Parameters, param)
					}

					// body parameters
					if requestBody, err := res.bodyparams(im.BodyParams, im, schemaComponentTypes); err != nil {
						return err
					} else if requestBody != nil {
						op.RequestBody = requestBody
					}

					// responses
					if responses, err := res.responses(im, m, schemaComponentTypes, neededArrayExamples, neededMapExamples, responsesWithoutExamples); err != nil {
						return err
					} else if responses != nil {
						op.Responses = responses
					} else {
						log.Printf("impl has no responses: %#v", im) // for debugging
					}

					op.Tags = im.Tags
					if len(op.Tags) < 1 {
						op.Tags = append(op.Tags, "untagged")
					}
					if strings.HasPrefix(r.Path, "/accounts/all/") || r.Path == "/accounts/all" {
						op.Tags = append(op.Tags, TagUnified)
					}
					op.Tags = append(op.Tags, "all")

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

	untagged := []string{}
	unified := []string{}
	for path, pathItem := range pathItemMap.FromOldest() {
		if ops := pathItem.GetOperations(); ops != nil {
			for verb, op := range ops.FromOldest() {
				if len(op.Tags) < 1 {
					untagged = append(untagged, fmt.Sprintf("%s (%s %s)", op.OperationId, verb, path))
				}
				if slices.Contains(op.Tags, "unified") {
					unified = append(unified, fmt.Sprintf("%s (%s %s)", op.OperationId, verb, path))
				}
			}
		}
	}

	componentResponses := orderedmap.New[string, *v3.Response]()
	{
		for code, t := range m.DefaultResponses {
			if t.Type == nil {
				continue
			}
			key := fmt.Sprintf("%s.%d", t.Type.Key(), code)
			contentMap := orderedmap.New[string, *v3.MediaType]()
			if schema, ext, err := res.schema(ResponseScope, fmt.Sprintf("default response '%s'", t.Type.Name()), t.Type, []string{t.Type.Name()}, schemaComponentTypes, ""); err != nil {
				return fmt.Errorf("failed to reference default response type %s: %v", t, err)
			} else {
				contentMap.Set("application/json", &v3.MediaType{
					Schema:     schema,
					Extensions: ext,
				})
			}

			summary := t.Summary
			if summary == "" {
				summary = tools.MustHttpStatusText(code)
			}

			headers := orderedmap.New[string, *v3.Header]()
			for k, h := range m.DefaultResponseHeaders {
				if !h.IsApplicable(code) {
					continue
				}
				headers.Set(k, v3.CreateHeaderRef(HeaderComponentRefPrefix+k))
			}
			response := &v3.Response{
				// Summary:     summary, // is ignored
				Description: summary,
				Content:     contentMap,
				Headers:     headers,
			}
			componentResponses.Set(key, response)
		}
	}

	// add the schema of types that are referenced as body parameters or responses
	componentSchemas := orderedmap.New[string, *highbase.SchemaProxy]()
	{
		schemas := map[string]*highbase.SchemaProxy{}

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

					{
						// we don't look into whether there are response exceptions here, since the regular reference
						// (just the type name, e.g. "jmap.Identity") is assumed to be the one to reference in requests
						ref := t.Key()
						ctx := fmt.Sprintf("resolving schema component type '%s'", t.Key())
						if schema, _, err := res.schema(ResponseScope, ctx, t, []string{}, moreSchemaComponentTypes, t.Summary()); err == nil {
							if schema != nil {
								schemas[ref] = schema
							}
						} else {
							return err
						}
					}

					if model.HasExceptions(t) {
						// for type schemas to reference in requests, we either use the one for responses that have been created above,
						// or in case of request or response field exceptions, we reference another schema that is specifically for
						// requests, with this suffix:
						ref := t.Key() + RequestExceptionTypeKeySuffix
						ctx := fmt.Sprintf("resolving schema component type '%s' for use in requests", t.Key())
						if schema, _, err := res.schema(RequestScope, ctx, t, []string{}, moreSchemaComponentTypes, t.Summary()); err == nil {
							if schema != nil {
								schemas[ref] = schema
							}
						} else {
							return err
						}
					}
				}

				{
					x := map[string]model.Type{}
					for k, v := range moreSchemaComponentTypes {
						if _, ok := schemas[k]; ok {
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
		mkeys := slices.Collect(maps.Keys(schemas))
		slices.Sort(mkeys)
		for _, k := range mkeys {
			if v, ok := schemas[k]; ok {
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
			header := &v3.Header{
				Description: desc.Summary,
				Schema:      highbase.CreateSchemaProxy(stringSchema(desc.Summary)),
				Required:    req,
				Explode:     explode,
			}
			if len(desc.Examples) > 0 {
				examples := orderedmap.New[string, *highbase.Example]()
				for k, e := range desc.Examples {
					examples.Set(k, &highbase.Example{
						Summary: k,
						Value:   &yaml.Node{Kind: yaml.ScalarNode, Value: e},
					})
				}
				header.Examples = examples
			}
			componentHeaders.Set(name, header)
		}
	}

	componentExamples := orderedmap.New[string, *highbase.Example]()
	{
		for _, examples := range m.Examples {
			if list, ok := examples.ForResponse(); ok {
				for _, e := range list {
					rendered := renderedExampleMap[e.Key].forResponse()
					// e.Key is e.g. "jmap.Identity:default"
					componentExamples.Set(e.Key, &highbase.Example{
						//Summary:     name, // not used
						Description: e.Title,
						Value:       rendered,
						Extensions: ext3(
							"oc-example-key", e.Key,
							"oc-example-origin", e.Origin,
							"oc-example-scope", string(ResponseScope),
						),
					})
				}
			}
		}

		for name := range neededArrayExamples {
			if examples, ok := m.Examples[name]; ok {
				rendereds := renderExamplesAsArrays(examples)
				maps.Copy(renderedExampleMap, rendereds)

				if list, ok := examples.ForResponse(); ok {
					for _, e := range list {
						rendered := rendereds[e.Key].forResponse()
						key := "[]" + e.Key
						componentExamples.Set(key, &highbase.Example{
							// Summary:     "Array of " + name, // not used
							Description: e.Title,
							Value:       rendered,
							Extensions: ext4(
								"oc-example-key", e.Key,
								"oc-example-origin", e.Origin,
								"oc-example-scope", string(ResponseScope),
								"oc-example-auto", "array",
							),
						})

					}
				}
			}
		}

		for name := range neededMapExamples {
			if examples, ok := m.Examples[name]; ok {
				rendereds := renderExamplesAsMaps("key", examples)
				maps.Copy(renderedExampleMap, rendereds)

				if list, ok := examples.ForResponse(); ok {
					for _, e := range list {
						rendered := rendereds[e.Key].forResponse()
						key := "map[string]" + e.Key
						componentExamples.Set(key, &highbase.Example{
							// Summary:     "Array of " + name, // not used
							Description: e.Title,
							Value:       rendered,
							Extensions: ext4(
								"oc-example-key", e.Key,
								"oc-example-origin", e.Origin,
								"oc-example-scope", string(ResponseScope),
								"oc-example-auto", "map",
							),
						})

					}
				}
			}
		}
	}

	components := &v3.Components{
		Schemas:   componentSchemas,
		Responses: componentResponses,
		Headers:   componentHeaders,
		Examples:  componentExamples,
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
			Version: m.Version,
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
		if template.Info != nil {
			if template.Info.Title != "" {
				doc.Info.Summary = template.Info.Title
			}
			if template.Info.Summary != "" {
				doc.Info.Summary = template.Info.Summary
			}
			if template.Info.Description != "" {
				doc.Info.Description = template.Info.Description
			}
			if template.Info.TermsOfService != "" {
				doc.Info.TermsOfService = template.Info.TermsOfService
			}
		}
	}

	if (len(untagged) < 1 || len(unified) < 1) && doc.Extensions != nil {
		if node, ok := doc.Extensions.Get("x-tagGroups"); ok && node != nil {
			keep := []*yaml.Node{}
			for _, m := range node.Content {
				skip := false
				if m.Kind == yaml.MappingNode && len(m.Content) > 0 {
					for i := 0; i < len(m.Content); {
						k := m.Content[i]
						i++
						v := m.Content[i]
						i++
						if k.Value == "name" && ((len(untagged) < 1 && v.Value == "Uncategorized") || (len(unified) < 1 && k.Value == "Unified")) {
							// skip
							skip = true
							break
						}
					}
				}
				if !skip {
					keep = append(keep, m)
				}
			}
			node.Content = keep
		}
	}

	// logging output to stderr
	if len(untagged) > 0 {
		log.Printf("%d untagged endpoints:", len(untagged))
		for _, u := range untagged {
			log.Printf("  - %s", u)
		}
	}
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
	if len(responsesWithoutExamples) > 0 {
		log.Printf("%d responses without examples:", len(responsesWithoutExamples))
		for name := range responsesWithoutExamples {
			log.Printf("  - %s", name)
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

func ext3(k1, v1, k2, v2, k3, v3 string) *orderedmap.Map[string, *yaml.Node] {
	ext := orderedmap.New[string, *yaml.Node]()
	ext.Set(k1, &yaml.Node{Kind: yaml.ScalarNode, Value: v1})
	ext.Set(k2, &yaml.Node{Kind: yaml.ScalarNode, Value: v2})
	ext.Set(k3, &yaml.Node{Kind: yaml.ScalarNode, Value: v3})
	return ext
}

func ext4(k1, v1, k2, v2, k3, v3, k4, v4 string) *orderedmap.Map[string, *yaml.Node] {
	ext := orderedmap.New[string, *yaml.Node]()
	ext.Set(k1, &yaml.Node{Kind: yaml.ScalarNode, Value: v1})
	ext.Set(k2, &yaml.Node{Kind: yaml.ScalarNode, Value: v2})
	ext.Set(k3, &yaml.Node{Kind: yaml.ScalarNode, Value: v3})
	ext.Set(k4, &yaml.Node{Kind: yaml.ScalarNode, Value: v4})
	return ext
}

func omap1[K comparable, V any](k K, v V) *orderedmap.Map[K, V] {
	m := orderedmap.New[K, V]()
	m.Set(k, v)
	return m
}
