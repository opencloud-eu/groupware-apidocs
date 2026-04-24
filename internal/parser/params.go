package parser

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"maps"
	"regexp"
	"strings"

	"golang.org/x/tools/go/packages"
	"opencloud.eu/groupware-apidocs/internal/config"
	"opencloud.eu/groupware-apidocs/internal/model"
	"opencloud.eu/groupware-apidocs/internal/tools"
)

var (
	parseOptionalParamCallRegex          = regexp.MustCompile(`^(?:parse|get)([A-Z].*?)Param$`)
	parseMandatoryParamCallRegex         = regexp.MustCompile(`^(?:parse|get)Mandatory([A-Z].*?)Param$`)
	parseOptionalSingleArgParamCallRegex = regexp.MustCompile(`^parseOpt([A-Z].*?)Param$`)
)

type paramsVisitor struct {
	fset                      *token.FileSet
	fun                       string
	endpoint                  model.Endpoint
	groupwarePkg              *packages.Package
	typeMap                   map[string]model.Type
	objectTypeMap             map[string]model.ObjectType
	exampleKey                string
	defaultValue              string
	headerParams              map[string]model.Param
	queryParams               map[string]model.Param
	pathParams                map[string]model.Param
	bodyParams                map[string]model.Param
	templateFuncs             map[string]model.TemplateFunc
	responses                 map[int]model.Resp
	potentialErrors           map[string]bool
	responseFuncs             map[string]responseFunc
	responseMemberFuncs       map[string]responseFunc
	undocumentedResults       map[string]token.Position
	undocumentedRequestBodies map[string]token.Position
	defaultSummary            string
	pathParamDefs             map[string]model.ParamDefinition
	queryParamDefs            map[string]model.ParamDefinition
	headerParamDefs           map[string]model.ParamDefinition
	errs                      *[]error
}

func newParamsVisitor(
	fset *token.FileSet,
	groupwarePkg *packages.Package,
	fun string,
	endpoint model.Endpoint,
	typeMap map[string]model.Type,
	objectTypeMap map[string]model.ObjectType,
	templateFuncs map[string]model.TemplateFunc,
	responseFuncs map[string]responseFunc,
	responseMemberFuncs map[string]responseFunc,
	exampleKey string,
	pathParamDefs map[string]model.ParamDefinition,
	queryParamDefs map[string]model.ParamDefinition,
	headerParamDefs map[string]model.ParamDefinition,
) paramsVisitor {
	return paramsVisitor{
		fset:                      fset,
		fun:                       fun,
		endpoint:                  endpoint,
		groupwarePkg:              groupwarePkg,
		typeMap:                   typeMap,
		objectTypeMap:             objectTypeMap,
		exampleKey:                exampleKey,
		headerParams:              map[string]model.Param{},
		queryParams:               map[string]model.Param{},
		pathParams:                map[string]model.Param{},
		bodyParams:                map[string]model.Param{},
		responses:                 map[int]model.Resp{},
		potentialErrors:           map[string]bool{},
		templateFuncs:             templateFuncs,
		responseFuncs:             responseFuncs,
		responseMemberFuncs:       responseMemberFuncs,
		undocumentedResults:       map[string]token.Position{},
		undocumentedRequestBodies: map[string]token.Position{},
		pathParamDefs:             pathParamDefs,
		queryParamDefs:            queryParamDefs,
		headerParamDefs:           headerParamDefs,
		errs:                      &[]error{},
		defaultSummary:            "",
	}
}

func (v paramsVisitor) isBodyCall(call *ast.CallExpr) (model.Param, bool, error) {
	if len(call.Args) != 1 && len(call.Args) != 2 {
		return model.Param{}, false, nil
	}

	_, _, funcName, ok := expandFQMethodCall(call, v.groupwarePkg, config.GroupwarePackageID, seqp("Request"), contp([]string{"body", "bodydoc", "optBody", "optBodyDoc"}))
	if !ok {
		return model.Param{}, false, nil
	}

	required := true
	if strings.HasPrefix(funcName, "opt") {
		required = false
	}
	exploded := false // TODO body parameters typically aren't exploded since they are JSON payloads but implement something if needed
	var bodyArg ast.Expr
	var descArg ast.Expr
	if len(call.Args) == 1 {
		bodyArg = call.Args[0]
	} else if len(call.Args) == 2 {
		bodyArg = call.Args[0]
		descArg = call.Args[1]
	} else {
		return model.Param{}, false, nil
	}

	desc := ""
	if descArg != nil {
		if value, ok := isString(descArg); ok {
			desc = value
		} else {
			return model.Param{}, false, fmt.Errorf("unsupported call to Request.bodydoc(): description argument is not a BasicLit STRING but a STRING %T: %v", descArg, descArg)
		}
	}

	switch a := bodyArg.(type) {
	case *ast.UnaryExpr:
		if ident, ok := isIdent(a.X); ok && a.Op == token.AND && ident.Obj != nil {
			if vs, ok := isValueSpec(ident.Obj.Decl); ok {
				if n, err := nameOf(vs.Type, v.groupwarePkg.Name); err == nil {
					if typ, err := toType(n, v.typeMap); err != nil {
						return model.Param{}, false, fmt.Errorf("failed to resolve type identifier '%s' from call to Request.body[doc]()", n)
					} else {
						exampleKey := v.exampleKey // TODO implement example key override using an inline comment
						defaultValue := ""         // TODO implement default value for Request.body() calls
						return model.NewParam(n, desc, typ, required, defaultValue, exploded, exampleKey), true, nil
					}
				} else {
					return model.Param{}, false, err
				}
			} else {
				return model.Param{}, false, fmt.Errorf("unsupported call to Request.body[doc](): UnaryExpr argument is not an Ident but a %v", a)
			}
		} else {
			return model.Param{}, false, fmt.Errorf("unsupported call to Request.body[doc](): UnaryExpr X is not an Ident but a %T: %v", a.X, a.X)
		}
	default:
		return model.Param{}, false, fmt.Errorf("unsupported call to Request.body[doc](): argument is not a UnaryExpr but a %T: %v", a, a)
	}
}

func (v paramsVisitor) isRespondCall(call *ast.CallExpr) (map[int]model.Resp, map[string]bool, map[string]token.Position, bool, error) {
	if len(call.Args) == 3 && isMethodCall(call, "Groupware", "respond") {
		arg := call.Args[2]
		if c, ok := isClosure(arg); ok {
			rv := newReturnVisitor(v.fset, v.groupwarePkg, v.typeMap, v.responseFuncs, v.responseMemberFuncs, v.exampleKey, v.defaultValue)
			ast.Walk(rv, c)
			if err := errors.Join(*rv.errs...); err != nil {
				return nil, nil, nil, false, err
			}
			if len(rv.responses) > 0 {
				return rv.responses, rv.potentialErrors, rv.undocumented, true, nil
			}
		}
	}
	return nil, nil, nil, false, nil
}

func (v paramsVisitor) isParseQueryParamCall(call *ast.CallExpr) (model.Param, bool, error) {
	typeDef := ""
	summary := ""
	required := false
	var nameArg ast.Expr
	if m := isMethodCallRegex(call, "Request", parseOptionalSingleArgParamCallRegex, 1); m != nil {
		typeDef = m[0][1]
		required = false
		nameArg = call.Args[0]
	} else if m := isMethodCallRegex(call, "Request", parseMandatoryParamCallRegex, 1); m != nil {
		typeDef = m[0][1]
		required = true
		nameArg = call.Args[0]
	} else if m := isMethodCallRegex(call, "Request", parseOptionalParamCallRegex, 1); m != nil {
		typeDef = m[0][1]
		required = false
		nameArg = call.Args[0]
	}

	name := ""
	if a, ok := isIdent(nameArg); ok {
		name = a.Name
	} else {
		return model.Param{}, false, nil
	}

	exploded := false
	var typ model.Type = nil
	if typeDef != "" {
		switch typeDef {
		case "String":
			typ = model.StringType
		case "Int":
			typ = model.IntType
		case "UInt":
			typ = model.UIntType
		case "Date":
			typ = model.StringType
		case "Bool":
			typ = model.BoolType
		case "StringList":
			typ = model.NewArrayType(model.StringType)
			exploded = true
		case "Map":
			typ = model.NewMapType(model.StringType, model.StringType)
		default:
			return model.Param{}, true, fmt.Errorf("unsupported type '%s' for query parameter through call to 'Request'", typeDef)
		}
	} else if len(call.Args) == 1 && tools.HasAnyPrefix(name, config.QueryParamPrefixes) &&
		isFQMethodCall(call, v.groupwarePkg, "net/url", seqp("Values"), seqp("Get")) {
		required = false
		typ = model.StringType
	} else {
		return model.Param{}, false, nil
	}

	if summary == "" {
		if cg := findInlineComment(call.Pos(), v.groupwarePkg); cg != nil && len(cg.List) > 0 {
			summary = strings.Join(processComments(lines(cg.List)), "\n") // TODO parse one-liner response function comment for api tags if needed
		}
	}
	exampleKey := v.exampleKey // TODO implement example key override using an inline comment

	defaultValue := "" // TODO implement default values in query parameter parsing calls

	// TODO resolve name (QueryParamXYZ) to its string value using model.QueryParams map

	return model.NewParam(name, summary, typ, required, defaultValue, exploded, exampleKey), true, nil
}

func (v paramsVisitor) isPathParamCall(call *ast.CallExpr) (model.Param, bool) {
	required := true
	if len(call.Args) == 2 && isStaticFunc(call, "chi", "URLParam") {
		if a, ok := isIdent(call.Args[1]); ok {
			summary := ""
			if cg := findInlineComment(call.Pos(), v.groupwarePkg); cg != nil && len(cg.List) > 0 {
				summary = strings.Join(processComments(lines(cg.List)), "\n") // TODO parse one-liner response function comment for api tags if needed
			}
			exampleKey := v.exampleKey // TODO implement example key override using an inline comment
			defaultValue := ""         // no default values for path parameters
			return model.NewParam(a.Name, summary, model.StringType, required, defaultValue, false, exampleKey), true
		} else {
			panic(fmt.Errorf("chi.URLParam first argument is not an Ident but a %T: %v", call.Args[0], call.Args[0]))
		}
	} else if _, _, f, ok := expandFQMethodCall(call, v.groupwarePkg, config.GroupwarePackageID, seqp("Request"), contp([]string{"PathParam", "PathParamDoc", "PathListParamDoc"})); ok {
		if f == "PathParam" && len(call.Args) == 1 {
			if a, ok := isIdent(call.Args[0]); ok {
				summary := ""
				if cg := findInlineComment(call.Pos(), v.groupwarePkg); cg != nil && len(cg.List) > 0 {
					summary = strings.Join(processComments(lines(cg.List)), "\n") // TODO parse one-liner response function comment for api tags if needed
				}
				exampleKey := v.exampleKey // TODO implement example key override using an inline comment
				defaultValue := ""         // no default values for path parameters
				return model.NewParam(a.Name, summary, model.StringType, required, defaultValue, false, exampleKey), true
			} else {
				panic(fmt.Errorf("Request.%s first argument is not an Ident but a %T: %v", f, call.Args[0], call.Args[0]))
			}
		} else if len(call.Args) == 2 {
			var typ model.Type = model.StringType
			if strings.HasPrefix(f, "PathList") {
				typ = model.NewArrayType(model.StringType)
			}
			if a, ok := isIdent(call.Args[0]); ok {
				if desc, ok := isString(call.Args[1]); ok {
					exampleKey := v.exampleKey // TODO implement example key override using an inline comment
					defaultValue := ""         // no default values for path parameters
					return model.NewParam(a.Name, desc, typ, required, defaultValue, true, exampleKey), true
				} else {
					panic(fmt.Errorf("Request.%s second argument is not a BasicLit STRING but a %T: %v", f, call.Args[1], call.Args[1]))
				}
			} else {
				panic(fmt.Errorf("Request.%s first argument is not an Ident but a %T: %v", f, call.Args[0], call.Args[0]))
			}
		}
	}
	// TODO PathParam methods that also cast to a different type (int, ...) than string, if needed
	// TODO optional PathParam methods where req=NotRequired, if needed (although path params should always be required, in theory)
	return model.Param{}, false
}

func (v paramsVisitor) isHeaderParamCall(call *ast.CallExpr) (model.Param, bool) {
	if len(call.Args) == 2 && isFQMethodCall(call, v.groupwarePkg, config.GroupwarePackageID, seqp("Request"), seqp("HeaderParamDoc")) {
		if a, ok := isIdent(call.Args[0]); ok {
			if summary, ok := isString(call.Args[1]); ok {
				exampleKey := v.exampleKey // TODO implement example key override using an inline comment
				defaultValue := ""         // TODO implement default value for header parameters
				return model.NewParam(a.Name, summary, model.StringType, true, defaultValue, false, exampleKey), true
			}
		}
	} else if len(call.Args) == 1 && isFQMethodCall(call, v.groupwarePkg, config.GroupwarePackageID, seqp("Request"), seqp("HeaderParam")) {
		if a, ok := isIdent(call.Args[0]); ok {
			summary := ""
			if cg := findInlineComment(call.Pos(), v.groupwarePkg); cg != nil && len(cg.List) > 0 {
				summary = strings.Join(processComments(lines(cg.List)), "\n") // TODO parse one-liner response function comment for api tags if needed
			}
			exampleKey := v.exampleKey // TODO implement example key override using an inline comment
			defaultValue := ""         // TODO implement default value for header parameters
			return model.NewParam(a.Name, summary, model.StringType, true, defaultValue, false, exampleKey), true
		}
	} else if len(call.Args) == 2 && isFQMethodCall(call, v.groupwarePkg, config.GroupwarePackageID, seqp("Request"), seqp("OptHeaderParamDoc")) {
		if a, ok := isIdent(call.Args[0]); ok {
			if summary, ok := isString(call.Args[1]); ok {
				exampleKey := v.exampleKey // TODO implement example key override using an inline comment
				defaultValue := ""         // TODO implement default value for header parameters
				return model.NewParam(a.Name, summary, model.StringType, false, defaultValue, false, exampleKey), true
			}
		}
	} else if len(call.Args) == 1 && isFQMethodCall(call, v.groupwarePkg, config.GroupwarePackageID, seqp("Request"), seqp("OptHeaderParam")) {
		if a, ok := isIdent(call.Args[0]); ok {
			summary := ""
			if cg := findInlineComment(call.Pos(), v.groupwarePkg); cg != nil && len(cg.List) > 0 {
				summary = strings.Join(processComments(lines(cg.List)), "\n") // TODO parse one-liner response function comment for api tags if needed
			}
			exampleKey := v.exampleKey // TODO implement example key override using an inline comment
			defaultValue := ""         // TODO implement default value for header parameters
			return model.NewParam(a.Name, summary, model.StringType, false, defaultValue, false, exampleKey), true
		}
	}
	return model.Param{}, false
}

func (v paramsVisitor) isBuildFilterCall(call *ast.CallExpr) (map[string]model.Param, map[string]model.Param, error) {
	if len(call.Args) != 1 {
		return nil, nil, nil
	}
	if _, ok := isSelector(call.Fun); !ok {
		return nil, nil, nil
	}
	if !isFQMethodCall(call, v.groupwarePkg, config.GroupwarePackageID, seqp("Groupware"), seqp("buildEmailFilter")) {
		return nil, nil, nil
	}

	var bf *ast.FuncDecl
	{
		for _, syn := range v.groupwarePkg.Syntax {
			for _, decl := range syn.Decls {
				if f := isFQFuncDecl(decl, v.groupwarePkg, "Groupware", "buildEmailFilter"); f != nil {
					bf = f
					break
				}
			}
		}
	}
	if bf == nil {
		return nil, nil, fmt.Errorf("failed to find declaration of 'buildEmailFilter' in the package '%s'", v.groupwarePkg.ID)
	}

	p := newParamsVisitor(
		v.fset, v.groupwarePkg, bf.Name.Name, v.endpoint,
		v.typeMap, v.objectTypeMap, v.templateFuncs, v.responseFuncs, v.responseMemberFuncs,
		v.exampleKey,
		v.pathParamDefs, v.queryParamDefs, v.headerParamDefs,
	)
	ast.Walk(p, bf.Body)
	if err := errors.Join(*v.errs...); err != nil {
		return nil, nil, err
	}

	return p.queryParams, p.headerParams, nil
}

type templateFuncSummaryModel struct {
	Name         string
	Names        string
	Verb         string
	Path         string
	ObjType      string
	UriParamName string
	AccountType  string
	QueryParam   map[string]model.ParamDefinition
	PathParam    map[string]model.ParamDefinition
	HeaderParam  map[string]model.ParamDefinition
	TypeParam    map[string]string
}

func (v paramsVisitor) Visit(n ast.Node) ast.Visitor {
	if n == nil { // = nothing left to visit
		return nil
	}
	switch d := n.(type) {
	/*
		use this to catch any reference to a QueryParam* or UriParam* constant within the body of a method:

		case *ast.Ident:
			if hasAnyPrefix(d.Name, QueryParamPrefixes) {
				v.queryParams[d.Name] = true
			} else if hasAnyPrefix(d.Name, PathParamPrefixes) {
				v.pathParams[d.Name] = Param{Name: d.Name, Description: "", Required: true}
			}
	*/
	case *ast.CallExpr:
		if p, ok, err := v.isBodyCall(d); err != nil {
			*v.errs = append(*v.errs, err)
			return nil
		} else if ok {
			v.bodyParams[p.Name] = p
			if p.Description == "" {
				pos := v.fset.Position(d.Pos())
				v.undocumentedRequestBodies[p.Name] = pos
			}
			return v
		}

		if p, ok := isAccountCall(d, v.groupwarePkg, v.exampleKey); ok {
			v.pathParams[p.Name] = p
			return v
		}

		if f, ok := isIdent(d.Fun); len(d.Args) > 0 && ok {
			if tf, ok := v.templateFuncs[f.Name]; ok {
				if x, ok := isIdent(d.Args[0]); ok {
					typeParams := map[string]model.Type{}
					{
						typePlaceholderNames := []string{}
						if def, ok := v.groupwarePkg.TypesInfo.Uses[f]; ok {
							if t, ok := def.Type().(*types.Signature); ok {
								for tparam := range t.TypeParams().TypeParams() {
									typePlaceholderNames = append(typePlaceholderNames, tparam.Obj().Name())
								}
							} else {
								panic(fmt.Errorf("failed to case to Signature"))
							}
						}
						if gen, ok := v.groupwarePkg.TypesInfo.Instances[f]; ok {
							for i, tparam := range typePlaceholderNames {
								arg := gen.TypeArgs.At(i)
								if p, ok := arg.(*types.Pointer); ok {
									arg = p.Elem()
								}
								switch arg := arg.(type) {
								case *types.Named:
									name := arg.Obj().Name()
									if arg.Obj().Pkg() != nil && arg.Obj().Pkg().Name() != "" {
										name = arg.Obj().Pkg().Name() + "." + name
									}
									if t, ok := v.typeMap[name]; !ok {
										*v.errs = append(*v.errs, fmt.Errorf("failed to map generic parameter 'T' to type '%s': not found in typemap", name))
										return nil
									} else {
										typeParams[tparam] = t
									}
								default:
									*v.errs = append(*v.errs, fmt.Errorf("failed to map generic parameter 'T': unsupported TypeArg: %T: %#+v", gen.TypeArgs.At(0), gen.TypeArgs.At(0)))
									return nil
								}
							}
						}
					}

					ot, ok := v.objectTypeMap[x.Name]
					if !ok {
						*v.errs = append(*v.errs, fmt.Errorf("failed to resolve function template's first argument ObjectType '%s' in %+v", x.Name, d.Fun))
						return nil
					}

					typeParamsNames := map[string]string{}
					for k, v := range typeParams {
						typeParamsNames[k] = v.Key()
					}

					m := templateFuncSummaryModel{
						Name:         ot.Name,
						Names:        ot.Plural,
						Verb:         v.endpoint.Verb,
						Path:         v.endpoint.Path,
						ObjType:      ot.Foo,
						UriParamName: ot.UriParamName,
						AccountType:  ot.AccountType,
						QueryParam:   v.queryParamDefs,
						PathParam:    v.pathParamDefs,
						HeaderParam:  v.headerParamDefs,
						TypeParam:    typeParamsNames,
					}

					if tf.BodyType != "" {
						bodyDesc := ""
						var bodyType model.Type = nil
						ary := false
						singularType := tf.BodyType
						if strings.HasPrefix(tf.BodyType, "[]") {
							ary = true
							singularType = tf.BodyType[2:]
						}
						if strings.ToUpper(singularType) == singularType {
							// generic parameter
							if g, ok := typeParams[singularType]; ok {
								bodyType = g
							} else {
								*v.errs = append(*v.errs, fmt.Errorf("failed to find generic type parameter %s", singularType))
								return nil
							}
						} else if tf.BodyType != "" {
							if t, err := toType(tf.BodyType, v.typeMap); err != nil {
								bodyType = t
							} else {
								*v.errs = append(*v.errs, err)
								return nil
							}
						}

						if bodyType != nil {
							if tf.BodyComment != nil {
								var buf bytes.Buffer
								if err := tf.BodyComment.Execute(&buf, m); err != nil {
									*v.errs = append(*v.errs, err)
									return nil
								} else {
									bodyDesc = buf.String()
								}
							}
							if ary {
								bodyType = model.NewArrayType(bodyType)
							}
							v.bodyParams[bodyType.Key()] = model.NewParam(bodyType.Key(), bodyDesc, bodyType, tf.BodyRequired, "", false, "")
						}
					}

					typ, ok := v.typeMap[ot.Foo]
					if !ok {
						*v.errs = append(*v.errs, fmt.Errorf("failed to resolve ObjectType '%s' Foo '%s' using the type map", ot.Name, ot.Foo))
						return nil
					}

					defaultSummary := fmt.Sprintf("%s %s %s", tools.Title(tf.Name), tools.Article(ot.Name), ot.Name)
					if tf.Comments != nil {
						var buf bytes.Buffer
						if err := tf.Comments.Execute(&buf, m); err != nil {
							*v.errs = append(*v.errs, err)
							return nil
						} else {
							defaultSummary = buf.String()
						}
					}
					v.defaultSummary = defaultSummary

					responseSummary := ""
					if r, ok := tf.Responses[tf.SuccessCode]; ok {
						var buf bytes.Buffer
						if err := r.Execute(&buf, m); err != nil {
							*v.errs = append(*v.errs, err)
							return nil
						} else {
							responseSummary = buf.String()
						}
					}

					responses := map[int]model.Resp{
						tf.SuccessCode: {
							Summary: responseSummary,
							Type:    typ,
						},
					}
					maps.Copy(v.responses, responses)
				} else {
					// not a call to a template function => ignore
				}
			}
		}

		if responses, potentialErrors, undocumented, ok, err := v.isRespondCall(d); err != nil {
			*v.errs = append(*v.errs, err)
			return nil
		} else if ok {
			maps.Copy(v.responses, responses)
			maps.Copy(v.potentialErrors, potentialErrors)
			maps.Copy(v.undocumentedResults, undocumented)
			return v
		}
		if p, _, ok := isNeedAccountCall(d, v.groupwarePkg, v.exampleKey); ok {
			v.pathParams[p.Name] = p
		}
		if p, ok := v.isPathParamCall(d); ok {
			v.pathParams[p.Name] = p
			return v
		}

		if p, ok, err := v.isParseQueryParamCall(d); err != nil {
			*v.errs = append(*v.errs, err)
			return nil
		} else if ok {
			v.queryParams[p.Name] = p
			return v
		}

		if p, ok := v.isHeaderParamCall(d); ok {
			v.headerParams[p.Name] = p
			return v
		}

		if queryParams, headerParams, err := v.isBuildFilterCall(d); err != nil {
			*v.errs = append(*v.errs, err)
			return nil
		} else {
			maps.Copy(v.queryParams, queryParams)
			maps.Copy(v.headerParams, headerParams)
		}
	}
	return v
}
