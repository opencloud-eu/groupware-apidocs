package parser

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"io/fs"
	"log"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"
	"opencloud.eu/groupware-apidocs/internal/config"
	"opencloud.eu/groupware-apidocs/internal/model"
)

var (
	objs          = regexp.MustCompile(`^([^/]+?s)/?$`)
	objById       = regexp.MustCompile(`^([^/]+?s)/{[^/]*?id}$`)
	objsInObjById = regexp.MustCompile(`^([^/]+?s)/{[^/]*?id}/([^/]+?s)$`)
	apiTag        = regexp.MustCompile(`^\s*@api:tags?\s+(.+)\s*$`)
)

func findRouteDefinition(a *ast.File) *ast.FuncDecl {
	for _, decl := range a.Decls {
		switch v := decl.(type) {
		case *ast.FuncDecl:
			if hasName(v, "Route") && isMemberOf(v, "Groupware") && hasNumParams(v, 1) && isParamType(v.Type.Params.List[0], "chi", "Router") {
				return v
			}
		}
	}
	return nil
}

func endpoints(base string, stmts []ast.Stmt, pkg string) ([]model.Endpoint, error) {
	result := []model.Endpoint{}
	for _, stmt := range stmts {
		if call := stmtIsCallExpr(stmt); call != nil {
			if verb, err := methodNameOf(call, "r"); err == nil && verb != "" {
				if verb == "Route" {
					// recurse
					if path := stringArgOf(call, 0); path != "" {
						if deep := funcArgOf(call, 1); deep != nil {
							if deref, err := endpoints(join(base, path), deep.Body.List, pkg); err == nil {
								result = append(result, deref...)
							} else {
								return nil, err
							}
						}
					}
				} else if slices.Contains(config.Verbs, verb) && len(call.Args) == 2 {
					if path := stringArgOf(call, 0); path != "" {
						if f := funcRefArgOf(call, 1); f != nil {
							if fun, err := nameOf(f.Sel, pkg); err == nil {
								result = append(result, model.Endpoint{Verb: strings.ToUpper(verb), Path: join(base, path), Fun: fun})
							} else {
								return nil, err
							}
						}
					}
				} else if slices.Contains(config.CustomVerbs, verb) && len(call.Args) == 3 {
					if path := stringArgOf(call, 1); path != "" {
						if f := funcRefArgOf(call, 2); f != nil {
							if fun, err := nameOf(f.Sel, pkg); err == nil {
								result = append(result, model.Endpoint{Verb: strings.ToUpper(verb), Path: join(base, path), Fun: fun})
							} else {
								return nil, err
							}
						}
					}
				}
			} else if err != nil {
				return nil, err
			}
		}
	}
	return result, nil
}

type GlobalParamDecls struct {
	pathParams   map[string]model.Param
	queryParams  map[string]model.Param
	headerParams map[string]model.Param
}

func scrapeGlobalParamDecls(decls []ast.Decl) (GlobalParamDecls, error) {
	pathParams := map[string]model.Param{}
	queryParams := map[string]model.Param{}
	headerParams := map[string]model.Param{}
	for _, decl := range decls {
		for _, c := range consts(decl) {
			if hasAnyPrefix(c.Name, config.PathParamPrefixes) {
				desc := describeParam(c.Comments)
				pathParams[c.Name] = model.Param{
					Name:        c.Value,
					Description: desc,
					Required:    true,
					Type:        model.NewBuiltinType("", "string"),
				}
			} else if hasAnyPrefix(c.Name, config.QueryParamPrefixes) {
				desc := describeParam(c.Comments)
				queryParams[c.Name] = model.Param{
					Name:        c.Value,
					Description: desc,
					Required:    false,
				}
			} else if hasAnyPrefix(c.Name, config.HeaderParamPrefixes) {
				desc := describeParam(c.Comments)
				headerParams[c.Name] = model.Param{
					Name:        c.Value,
					Description: desc,
					Required:    false,
				}
			}
		}
	}
	return GlobalParamDecls{
		pathParams:   pathParams,
		queryParams:  queryParams,
		headerParams: headerParams,
	}, nil
}

func tags(_ string, path string, comments []string) []string {
	tags := []string{}

	for _, line := range comments {
		if m := apiTag.FindAllStringSubmatch(line, 1); m != nil {
			for tag := range strings.SplitSeq(m[0][1], ",") {
				tag = strings.TrimSpace(tag)
				if len(tag) > 0 {
					tags = append(tags, tag)
				}
			}
		}
	}

	// TODO if tags have been defined manually, should we autodiscover as well? currently assuming no.
	if len(tags) > 0 {
		return tags
	}

	obj := path
	if strings.HasPrefix(path, "/accounts/{accountid}/") {
		obj = path[len("/accounts/{accountid}"):]
	} else if strings.HasPrefix(path, "/accounts/all/") {
		obj = path[len("/accounts/all"):]
	}

	obj = strings.TrimPrefix(obj, "/")

	if len(obj) > 0 {
		if m := objsInObjById.FindAllStringSubmatch(obj, -1); m != nil {
			tags = append(tags, singularize(m[0][2]))
		} else {
			words := strings.Split(obj, "/")
			if len(words) > 0 {
				tags = append(tags, singularize(words[0]))
			}
		}
	}
	return tags
}

func decomment(str string) string {
	if cut, ok := strings.CutPrefix(str, "// "); ok {
		return cut
	}
	return strings.TrimPrefix(str, "//")
}

func describeParam(comments []string) string {
	if len(comments) < 1 {
		return ""
	}

	i := 0
	line := decomment(comments[i])
	for ; i < len(comments); i++ {
		if len(strings.TrimSpace(line)) >= 0 {
			break
		}
	}
	keep := []string{}
	for ; i < len(comments); i++ {
		if strings.HasPrefix(line, "@api:") {
			continue
		}
		keep = append(keep, line)
		line = decomment(comments[i])
	}
	return strings.Join(keep, "\n")
}

func skipEmptyLines(i int, lines []string) int {
	for ; i < len(lines); i++ {
		if !strings.HasPrefix(strings.TrimSpace(lines[i]), "swagger:") && len(strings.TrimSpace(lines[i])) > 0 {
			return i
		}
	}
	return -1
}

func summarizeType(comments []string) (string, string) {
	if len(comments) < 1 {
		return "", ""
	}

	i := 0
	i = skipEmptyLines(i, comments)
	if i < 0 {
		return "", ""
	}

	summary := comments[i]
	description := ""
	i++
	if i < len(comments) {
		i = skipEmptyLines(i, comments)
		if i >= 0 && i < len(comments) {
			description = strings.Join(comments[i:], "\n")
		}
	}

	summary = title(summary)
	description = title(description)

	return summary, description
}

func summarizeEndpoint(verb string, path string, comments []string) (string, string) {
	summary := ""
	description := ""
	if len(comments) > 0 {
		i := 0
		summary = decomment(comments[i])
		for i < len(comments) && len(strings.TrimSpace(summary)) == 0 { // skip empty lines at the beginning
			i++
			if i < len(comments) {
				summary = decomment(comments[i])
			}
		}
		if len(strings.TrimSpace(summary)) < 1 { // skip empty lines at the beginning
			i++
			summary = decomment(comments[i])
		}
		if strings.HasPrefix(summary, "swagger:route") {
			i++
			summary = decomment(comments[i])
		}
		for i++; i < len(comments); i++ {
			line := comments[i]
			trimmed := strings.TrimSpace(line)
			if len(trimmed) > 0 {
				break
			}
		}
		keep := []string{}
		for ; i < len(comments); i++ {
			line := decomment(comments[i])
			trimmed := strings.TrimSpace(line)
			if trimmed == "responses:" {
				break
			}
			if strings.HasPrefix(trimmed, "@api:") {
				continue
			}
			keep = append(keep, line)
		}
		description = strings.Join(keep, "\n")
	}

	if summary == "" {
		forAccount := false
		forAllAccounts := false
		obj := path
		if strings.HasPrefix(path, "/accounts/{accountid}/") {
			forAccount = true
			obj = path[len("/accounts/{accountid}/"):]
		} else if strings.HasPrefix(path, "/accounts/all/") {
			forAccount = true
			forAllAccounts = true
			obj = path[len("/accounts/all/"):]
		}

		action := ""
		switch verb {
		case "GET":
			action = "retrieve"
		case "POST":
			action = "create"
		case "PUT":
			action = "replace"
		case "PATCH":
			action = "modify"
		case "DELETE":
			action = "delete"
		default:
			log.Panicf("unsupported verb for summarization: '%s'", verb)
			//return summary, description
		}

		if m := objById.FindAllStringSubmatch(obj, -1); m != nil {
			n := ""
			obj = m[0][1]
			if voweled(obj) {
				n = "n"
			}
			obj = singularize(obj)
			summary = fmt.Sprintf("%s a%s %s by its identifier", action, n, obj)
		} else if m := objs.FindAllStringSubmatch(obj, -1); m != nil {
			qual := "a"
			obj := m[0][1]
			switch verb {
			case "GET":
				qual = "all"
			default:
				obj = singularize(obj)
				if voweled(obj) {
					qual = "an"
				}
			}
			summary = fmt.Sprintf("%s %s %s", action, qual, obj)
		} else if m := objsInObjById.FindAllStringSubmatch(obj, -1); m != nil {
			n := ""
			if voweled(m[0][1]) {
				n = "n"
			}
			parent := m[0][1]
			child := singularize(m[0][2])
			summary = fmt.Sprintf("%s a %s of a %s%s by its identifier", action, child, parent, n)
		}
		if summary != "" {
			if forAllAccounts {
				summary = summary + " for all accounts"
			} else if forAccount {
				summary = summary + " for a given account"
			}
		}
	}

	summary = title(summary)
	description = title(description)

	summary = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(summary), "."))

	return summary, description
}

type responseFunc struct {
	hasBody    bool
	bodyArgPos int
	statusCode int
}

type returnVisitor struct {
	fset          *token.FileSet
	typeMap       map[string]model.Type
	pkg           *packages.Package
	responseTypes map[int]model.Type
	responseFuncs map[string]responseFunc
	undocumented  map[string]token.Position
}

func newReturnVisitor(fset *token.FileSet, pkg *packages.Package, typeMap map[string]model.Type, responseFuncs map[string]responseFunc) returnVisitor {
	return returnVisitor{
		fset:          fset,
		pkg:           pkg,
		typeMap:       typeMap,
		responseTypes: map[int]model.Type{},
		responseFuncs: responseFuncs,
		undocumented:  map[string]token.Position{},
	}
}

func (v returnVisitor) isResponseFunc(r *ast.ReturnStmt) (ast.Expr, int, bool) {
	if r == nil {
		return nil, 0, false
	}
	if c, ok := isCallExpr(r.Results[0]); ok {
		if i, ok := isIdent(c.Fun); ok {
			for f, spec := range v.responseFuncs {
				if i.Name == f {
					if spec.hasBody {
						return c.Args[spec.bodyArgPos], spec.statusCode, true
					} else {
						return nil, spec.statusCode, true
					}
				}
			}
		}
	}
	return nil, 0, false
}

func (v returnVisitor) resolveIdent(x *ast.Ident) (model.Type, error) {
	z, ok := v.pkg.TypesInfo.Uses[x]
	if !ok {
		z, ok = v.pkg.TypesInfo.Defs[x]
	}
	if !ok {
		return nil, fmt.Errorf("failed to find in TypesInfo.Uses or TypesInfo.Defs: %#v", x)
	}
	t := z.Type()
	return v.resolveType(t)
}

func (v returnVisitor) resolveType(t types.Type) (model.Type, error) {
	name := ""
	var pos token.Pos
	summary := ""
	description := ""
	switch n := t.(type) {
	case *types.Named:
		summary, description = summarizeType(findComments(n.Obj().Pos(), token.TYPE, v.pkg))
		{
			p := n.Obj().Pkg()
			if p != nil {
				name = p.Name() + "." + n.Obj().Name()
			} else {
				name = n.Obj().Name()
			}
		}
		pos = n.Obj().Pos()
	}
	r, err := typeOf(t, summary, description, v.typeMap, v.pkg)
	if err != nil {
		return nil, err
	}
	if r == nil {
		log.Panicf("failed to resolve type %v", t)
	} else {
		if r.Summary() == "" {
			if name != "" {
				v.undocumented[name] = v.pkg.Fset.Position(pos)
			}
		}
	}
	return r, nil
}

func (v returnVisitor) Visit(n ast.Node) ast.Visitor {
	if n == nil { // = nothing left to visit
		return nil
	}
	r, ok := n.(*ast.ReturnStmt)
	if !ok {
		return v
	}
	arg, code, ok := v.isResponseFunc(r)
	if !ok {
		return v
	}
	if arg == nil {
		// if a is nil, it means that there is no body
		v.responseTypes[code] = nil
		return v
	}

	switch arg := arg.(type) {
	case *ast.SelectorExpr:
		key := arg.X.(*ast.Ident).Name + "." + arg.Sel.Name
		if m, ok := v.typeMap[key]; ok {
			v.responseTypes[code] = m
		} else {
			fmt.Fprintf(os.Stderr, "failed to find the type '%s' in the typeMap, has the following types:\n", key)
			for _, k := range keysort(v.typeMap) {
				fmt.Fprintf(os.Stderr, "  - %s\n", k)
			}
			buf := new(bytes.Buffer)
			if err := ast.Fprint(buf, v.fset, arg, nil); err == nil {
				log.Panicf("failed to find the type '%s' in the typeMap, used as the response type for %#v in %s", key, arg, buf.String())
			} else {
				log.Panicf("failed to find the type '%s' in the typeMap, used as the response type for %#v", key, arg)
			}
		}
	case *ast.CompositeLit:
		switch t := arg.Type.(type) {
		case *ast.Ident:
			if t.Obj.Kind != ast.Typ {
				log.Panicf("unsupported compositelit type is an ident but not of kind Typ: %T: %#v", t, t)
			} else {
				key := t.Obj.Name
				if !strings.Contains(key, ".") {
					if !model.IsBuiltinType(key) {
						key = v.pkg.Name + "." + key
					}
				}
				if m, ok := v.typeMap[key]; ok {
					v.responseTypes[code] = m
				} else {
					fmt.Fprintf(os.Stderr, "failed to find the type '%s' in the typeMap, has the following types:\n", key)
					for _, k := range keysort(v.typeMap) {
						fmt.Fprintf(os.Stderr, "  - %s\n", k)
					}
					buf := new(bytes.Buffer)
					if err := ast.Fprint(buf, v.fset, arg, nil); err == nil {
						log.Panicf("failed to find the type '%s' in the typeMap, used as the response type for %#v in %s", key, arg, buf.String())
					} else {
						log.Panicf("failed to find the type '%s' in the typeMap, used as the response type for %#v", key, arg)
					}
				}
			}
		default:
			log.Panicf("unsupported compositelit type: %T: %#v", arg.Type, arg.Type)
		}
	case *ast.Ident:
		if arg.Obj == nil {
			log.Panicf("response body argument is an ident that has a nil Obj: %v", arg)
		} else {
			switch d := arg.Obj.Decl.(type) {
			case *ast.AssignStmt:
				var a *ast.Ident = nil
				for _, l := range d.Lhs {
					if v, ok := isIdent(l); ok {
						if v.Name == arg.Name {
							a = v
							break
						}
					}
				}
				if a == nil {
					log.Panicf("failed to find matching variable on LHS of assign statement") // TODO return proper err
				} else {
					if t, err := v.resolveIdent(a); err != nil {
						panic(err) // TODO collect and abort instead
					} else {
						v.responseTypes[code] = t
					}
				}
			case *ast.ValueSpec:
				if len(d.Names) == 1 {
					if t, ok := isIdent(d.Names[0]); ok {
						if strings.HasPrefix(t.Name, "RBODY") {
							suffix := t.Name[5:]
							if suffix != "" {
								var err error
								code, err = strconv.Atoi(suffix)
								if err != nil {
									log.Panicf("failed to parse integer status code in variable name '%v': %s\n", t.Name, err)
								}
							}
						}

						if t, err := v.resolveIdent(arg); err != nil {
							panic(err) // TODO collect and abort instead
						} else {
							v.responseTypes[code] = t
						}
					} else {
						log.Panicf("body result return variable is not an ident: %#v", d)
					}
				} else {
					log.Panicf("d.Names len is not == 1 but %d", len(d.Names))
				}
			default:
				buf := new(bytes.Buffer)
				if err := ast.Fprint(buf, v.fset, arg, nil); err == nil {
					log.Panicf("unsupported return decl: %T %v: %s", d, d, buf.String())
				} else {
					log.Panicf("unsupported return decl: %T %v", d, d)
				}
			}
		}
	case *ast.CallExpr:
		if i, ok := isIdent(arg.Fun); ok {
			switch d := i.Obj.Decl.(type) {
			case *ast.FuncDecl:
				if d.Type.Results != nil && len(d.Type.Results.List) > 0 {
					ret := d.Type.Results.List[0].Type
					if tv, ok := v.pkg.TypesInfo.Types[ret]; ok {
						if t, err := v.resolveType(tv.Type); err != nil {
							panic(err)
						} else if t == nil {
							panic("failed to resolve tv")
						} else {
							v.responseTypes[code] = t
						}
					} else {
						panic("failed to find return type expr in TypesInfo")
					}
				} else {
					panic("callexpr fun has no results")
				}
			default:
				panic("callexpr fun is not a funcdecl")
			}

		} else {
			panic("callexpr fun is not an ident")
		}
	case *ast.IndexExpr:
		switch x := arg.X.(type) {
		case *ast.SelectorExpr:
			if sel, ok := v.pkg.TypesInfo.Selections[x]; ok {
				p := sel.Obj().Type()
				switch u := p.(type) {
				case *types.Slice:
					switch n := u.Elem().(type) {
					case *types.Named:
						if t, err := v.resolveType(n); err != nil {
							panic(err)
						} else {
							v.responseTypes[code] = t
						}
					default:
						panic("slice elem is not named")
					}
				default:
					panic("sel is not a slice")
				}
			} else {
				def, ok := v.pkg.TypesInfo.Defs[x.Sel]
				if ok {
					buf := new(bytes.Buffer)
					ast.Fprint(buf, v.fset, arg, nil)
					log.Printf("definition of %s:\n%s", x.X.(*ast.Ident).Name, def.Name())
				} else {
					tv, ok := v.pkg.TypesInfo.Types[x.Sel]
					if !ok {
						panic("failed to find something")
					}
					log.Printf("found type: %v", tv)
				}
			}
		}
	default:
		buf := new(bytes.Buffer)
		ast.Fprint(buf, v.fset, arg, nil)
		panic("what's this?\n" + buf.String()) // TODO remove debugging
	}
	return v
}

type paramsVisitor struct {
	fset                      *token.FileSet
	fun                       string
	pkg                       *packages.Package
	typeMap                   map[string]model.Type
	headerParams              map[string]model.Param
	queryParams               map[string]model.Param
	pathParams                map[string]model.Param
	bodyParams                map[string]model.Param
	responseTypes             map[int]model.Type
	responseFuncs             map[string]responseFunc
	undocumentedResults       map[string]token.Position
	undocumentedRequestBodies map[string]token.Position
	errs                      *[]error
}

func newParamsVisitor(fset *token.FileSet, pkg *packages.Package, fun string, typeMap map[string]model.Type, responseFuncs map[string]responseFunc) paramsVisitor {
	return paramsVisitor{
		fset:                      fset,
		fun:                       fun,
		pkg:                       pkg,
		typeMap:                   typeMap,
		headerParams:              map[string]model.Param{},
		queryParams:               map[string]model.Param{},
		pathParams:                map[string]model.Param{},
		bodyParams:                map[string]model.Param{},
		responseTypes:             map[int]model.Type{},
		responseFuncs:             responseFuncs,
		undocumentedResults:       map[string]token.Position{},
		undocumentedRequestBodies: map[string]token.Position{},
		errs:                      &[]error{},
	}
}

func isValueSpec(expr any) (*ast.ValueSpec, bool) {
	switch e := expr.(type) {
	case *ast.ValueSpec:
		return e, true
	}
	return nil, false
}

func (v paramsVisitor) isBodyCall(call *ast.CallExpr) (string, string, bool, error) {
	if s, ok := isSelector(call.Fun); ok {
		if x, ok := isIdent(s.X); ok {
			if s.Sel.Name == "body" && len(call.Args) == 1 && x.Obj != nil {
				if f, ok := isField(x.Obj.Decl); ok {
					if t, ok := isIdent(f.Type); ok && t.Name == "Request" {
						a := call.Args[0]
						switch e := a.(type) {
						case *ast.UnaryExpr:
							if x, ok := isIdent(e.X); ok && e.Op == token.AND && x.Obj != nil {
								if vs, ok := isValueSpec(x.Obj.Decl); ok {
									if n, err := nameOf(vs.Type, v.pkg.Name); err == nil {
										return n, "", true, nil
									} else {
										return "", "", false, err
									}
								} else {
									return "", "", false, fmt.Errorf("unsupported call to Request.body(): UnaryExpr argument is not an Ident but a %v", e)
								}
							}
						default:
							return "", "", false, fmt.Errorf("unsupported call to Request.body(): is not a UnaryExpr but a %v", e)
						}
					} else {
						return "", "", false, fmt.Errorf("call to body() but not on a Request: %v", f)
					}
				} else {
					return "", "", false, fmt.Errorf("call to body() but is not a field: %v", x.Obj)
				}
			} else if s.Sel.Name == "bodydoc" && len(call.Args) == 2 && x.Obj != nil {
				if f, ok := isField(x.Obj.Decl); ok {
					if t, ok := isIdent(f.Type); ok && t.Name == "Request" {
						a := call.Args[0]
						switch e := a.(type) {
						case *ast.UnaryExpr:
							if x, ok := isIdent(e.X); ok && e.Op == token.AND && x.Obj != nil {
								if vs, ok := isValueSpec(x.Obj.Decl); ok {
									if n, err := nameOf(vs.Type, v.pkg.Name); err == nil {
										desc := ""
										if b, ok := isString(call.Args[1]); ok {
											desc = b
										}
										return n, desc, true, nil
									} else {
										return "", "", false, err
									}
								} else {
									return "", "", false, fmt.Errorf("unsupported call to Request.bodydoc(): UnaryExpr argument is not an Ident but a %v", e)
								}
							}
						default:
							return "", "", false, fmt.Errorf("unsupported call to Request.bodydoc(): is not a UnaryExpr but a %v", e)
						}
					} else {
						return "", "", false, fmt.Errorf("call to bodydoc() but not on a Request: %v", f)
					}
				} else {
					return "", "", false, fmt.Errorf("call to bodydoc() but is not a field: %v", x.Obj)
				}
			}
		}
	}
	return "", "", false, nil
}

func isClosure(expr ast.Expr) (*ast.FuncLit, bool) {
	switch e := expr.(type) {
	case *ast.FuncLit:
		return e, true
	}
	return nil, false
}

func (v paramsVisitor) isRespondCall(call *ast.CallExpr) (map[int]model.Type, map[string]token.Position, bool) {
	if s, ok := isSelector(call.Fun); ok {
		if x, ok := isIdent(s.X); ok && s.Sel.Name == "respond" && len(call.Args) == 3 && x.Obj != nil {
			if f, ok := isField(x.Obj.Decl); ok {
				if pkg, ok := isIdent(f.Type); ok && pkg.Name == "Groupware" {
					arg := call.Args[2]
					if c, ok := isClosure(arg); ok {
						rv := newReturnVisitor(v.fset, v.pkg, v.typeMap, v.responseFuncs)
						ast.Walk(rv, c)
						if len(rv.responseTypes) > 0 {
							return rv.responseTypes, rv.undocumented, true
						}
					}
				}
			}
		}
	}
	return nil, nil, false
}

func (v paramsVisitor) isAccountCall(call *ast.CallExpr) bool {
	if s, ok := isSelector(call.Fun); ok {
		if x, ok := isIdent(s.X); ok && (strings.HasPrefix(s.Sel.Name, "GetAccountFor") || strings.HasPrefix(s.Sel.Name, "GetAccountIdFor")) && len(call.Args) == 0 && x.Obj != nil {
			if f, ok := isField(x.Obj.Decl); ok {
				if t, ok := isIdent(f.Type); ok && t.Name == "Request" {
					return true
				}
			}
		}
	}
	return false
}

var need = regexp.MustCompile(`^need(Contact|Calendar|Task)WithAccount$`)

func (v paramsVisitor) isNeedAccountCall(call *ast.CallExpr) (string, string, bool) {
	if s, ok := isSelector(call.Fun); ok {
		if x, ok := isIdent(s.X); ok && len(call.Args) == 0 && x.Obj != nil {
			if m := need.FindAllStringSubmatch(s.Sel.Name, 2); m != nil {
				if f, ok := isField(x.Obj.Decl); ok {
					if t, ok := isIdent(f.Type); ok && t.Name == "Request" {
						return config.AccountIdUriParamName, "", true
					}
				}
			}
		}
	}
	return "", "", false
}

var parse = regexp.MustCompile(`^parse([A-Z].*?)Param$`)

func (v paramsVisitor) isParseQueryParamCall(call *ast.CallExpr) (string, string, model.Type, bool, error) {
	if s, ok := isSelector(call.Fun); ok {
		if x, ok := isIdent(s.X); ok && len(call.Args) == 2 && x.Obj != nil {
			if m := parse.FindAllStringSubmatch(s.Sel.Name, 2); m != nil {
				var typ model.Type = nil
				{
					switch m[0][1] {
					case "Int":
						typ = model.IntType
					case "UInt":
						typ = model.UIntType
					case "Date":
						typ = model.StringType
					case "Bool":
						typ = model.BoolType
					case "Map":
						typ = model.NewMapType(model.StringType, model.StringType)
					default:
						return "", "", nil, true, fmt.Errorf("unsupported type '%s' for query parameter through call to '%s'", m[0][1], s.Sel.Name)
					}
				}

				if f, ok := isField(x.Obj.Decl); ok {
					if t, ok := isIdent(f.Type); ok && t.Name == "Request" {
						if a, ok := isIdent(call.Args[0]); ok {
							return a.Name, "", typ, true, nil
						}
					}
				}
			}
		}
	}
	return "", "", nil, false, nil
}

func (v paramsVisitor) isPathParamCall(call *ast.CallExpr) (string, string, model.Type, bool) {
	if len(call.Args) == 2 && isStaticFunc(call, "chi", "URLParam") {
		if a, ok := isIdent(call.Args[1]); ok {
			return a.Name, "", nil, true
		}
	} else if len(call.Args) == 2 && isMemberFunc(call, "Request", "PathParamDoc") {
		if a, ok := isIdent(call.Args[0]); ok {
			if b, ok := isString(call.Args[1]); ok {
				return a.Name, b, model.StringType, true
			}
		}
	} else if len(call.Args) == 1 && isMemberFunc(call, "Request", "PathParam") {
		if a, ok := isIdent(call.Args[0]); ok {
			return a.Name, "", model.StringType, true
		}
	}
	// TODO PathParam methods that also cast to a different type (int, ...), if needed
	return "", "", nil, false
}

func (v paramsVisitor) isHeaderParamCall(call *ast.CallExpr) (string, string, bool, bool) {
	if len(call.Args) == 2 && isMemberFunc(call, "Request", "HeaderParamDoc") {
		if a, ok := isIdent(call.Args[0]); ok {
			if b, ok := isString(call.Args[1]); ok {
				return a.Name, b, true, true
			}
		}
	} else if len(call.Args) == 1 && isMemberFunc(call, "Request", "HeaderParam") {
		if a, ok := isIdent(call.Args[0]); ok {
			return a.Name, "", true, true
		}
	} else if len(call.Args) == 2 && isMemberFunc(call, "Request", "OptHeaderParamDoc") {
		if a, ok := isIdent(call.Args[0]); ok {
			if b, ok := isString(call.Args[1]); ok {
				return a.Name, b, false, true
			}
		}
	} else if len(call.Args) == 1 && isMemberFunc(call, "Request", "OptHeaderParam") {
		if a, ok := isIdent(call.Args[0]); ok {
			return a.Name, "", false, true
		}
	}
	return "", "", false, false
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
		if t, desc, ok, err := v.isBodyCall(d); err != nil {
			*v.errs = append(*v.errs, err)
			return nil
		} else if ok {
			v.bodyParams[t] = model.Param{Name: t, Description: desc, Required: false}
			if desc == "" {
				pos := v.fset.Position(d.Pos())
				v.undocumentedRequestBodies[t] = pos
			}
			return v
		}

		if v.isAccountCall(d) {
			v.pathParams[config.AccountIdUriParamName] = model.Param{Name: config.AccountIdUriParamName, Description: "", Required: true, Type: model.StringType}
			return v
		}

		if r, undocumented, ok := v.isRespondCall(d); ok {
			maps.Copy(v.responseTypes, r)
			maps.Copy(v.undocumentedResults, undocumented)
			return v
		}
		if z, desc, ok := v.isNeedAccountCall(d); ok {
			v.pathParams[z] = model.Param{Name: z, Description: desc, Required: true, Type: model.StringType}
			return v
		}
		if z, desc, typ, ok := v.isPathParamCall(d); ok {
			v.pathParams[z] = model.Param{Name: z, Description: desc, Required: true, Type: typ}
			return v
		}

		if z, desc, typ, ok, err := v.isParseQueryParamCall(d); err != nil {
			*v.errs = append(*v.errs, err)
			return nil
		} else if ok {
			v.queryParams[z] = model.Param{Name: z, Description: desc, Required: false, Type: typ}
			return v
		}

		if z, desc, req, ok := v.isHeaderParamCall(d); ok {
			v.headerParams[z] = model.Param{Name: z, Description: desc, Required: req}
			return v
		}
	}
	return v
}

var commentRegex = regexp.MustCompile(`^\s*//+\s?(.*)\s*$`)

func parseComment(text string) string {
	m := commentRegex.FindAllStringSubmatch(text, 2)
	if m != nil {
		return m[0][1]
	} else {
		return text
	}
}

func recv(s *types.Signature, pkg string, name string) bool {
	if s == nil {
		return false
	}
	r := s.Recv()
	if r == nil {
		return false
	}
	t := r.Type()
	if t == nil {
		return false
	}
	p, ok := t.(*types.Pointer)
	if ok {
		t = p.Elem()
	}
	n, ok := t.(*types.Named)
	if !ok {
		return false
	}
	return n.Obj().Name() == name && n.Obj().Pkg() != nil && n.Obj().Pkg().Name() == pkg
}

func p(v *types.Var, pkg string, name string) bool {
	t := v.Type()
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	switch n := t.(type) {
	case *types.Named:
		return n.Obj() != nil && n.Obj().Name() == name && n.Obj().Pkg() != nil && n.Obj().Pkg().Path() == pkg
	}
	return false
}

func isRouteFun(f *types.Func) bool {
	if !f.Exported() {
		return false
	}
	if slices.Contains(config.MiddlewareFunctionNames, f.Name()) {
		return false
	}
	s := f.Signature()
	if s.Results() != nil && s.Results().Len() != 0 { // must have no results
		return false
	}
	if !recv(s, "groupware", "Groupware") { // must be a pointer method of groupware.Groupware
		return false
	}
	if s.Params() == nil || s.Params().Len() != 2 { // must have 2 parameters
		return false
	}
	matches := p(s.Params().At(0), "net/http", "ResponseWriter") && p(s.Params().At(1), "net/http", "Request")
	return matches
}

func findFun(n string, p *packages.Package, _ *types.Func) (*ast.FuncDecl, string, bool) {
	for i, f := range p.CompiledGoFiles {
		s := p.Syntax[i] // TODO doesn't necessarily fit, can have nils that are compacted away
		if s == nil {
			continue
		}
		for _, d := range s.Decls {
			switch x := d.(type) {
			case *ast.FuncDecl:
				fname := s.Name.Name + "." + x.Name.Name
				if n == fname {
					return x, f, true
				}
				/*
					if fun.Pos() <= x.Pos() && x.End() >= fun.Pos() {
						return x, f, true
					}
				*/
			}
		}
	}
	return nil, "", false
}

func findTopDecl(t token.Pos, p *packages.Package) ast.Decl {
	pos := p.Fset.Position(t)
	for _, s := range p.Syntax {
		fp := p.Fset.Position(s.FileStart)
		if fp.Filename != pos.Filename {
			continue
		}
		for _, d := range s.Decls {
			dp := p.Fset.Position(d.Pos())
			if pos.Line == dp.Line {
				return d
			}
		}
	}
	return nil
}

func findCommentGroup(t token.Pos, p *packages.Package) *ast.CommentGroup {
	pos := p.Fset.Position(t)
	for _, s := range p.Syntax {
		fp := p.Fset.Position(s.FileStart)
		if fp.Filename != pos.Filename {
			continue
		}
		for _, g := range s.Comments {
			gp := p.Fset.Position(g.Pos())
			ge := p.Fset.Position(g.End())
			if pos.Line == gp.Line || ge.Line == pos.Line-1 {
				return g
			}
		}
	}
	return nil
}

func findComments(pos token.Pos, tokenType token.Token, pkg *packages.Package) []string {
	d := findTopDecl(pos, pkg)
	if d != nil {
		switch x := d.(type) {
		case *ast.GenDecl:
			if x.Tok == tokenType {
				if x.Doc != nil {
					return lines(x.Doc.List)
				} else {
					return []string{}
				}
			}
		}
	}
	c := findCommentGroup(pos, pkg)
	if c != nil {
		return lines(c.List)
	}
	return []string{}
}

func lines(s []*ast.Comment) []string {
	return collect(s, func(c *ast.Comment) string { return decomment(c.Text) })
}

var exampleFilenameWithPackageRegex = regexp.MustCompile(`^example\.(.+?)\.(.+?)\.(.+?)\.json$`)

func Parse(chdir string, basepath string) (model.Model, error) {
	routeFuncs := map[string]*types.Func{}
	typeMap := map[string]model.Type{}
	constsMap := map[string]bool{}
	routes := []model.Endpoint{}
	pathParams := map[string]model.Param{}
	queryParams := map[string]model.Param{}
	headerParams := map[string]model.Param{}
	ims := []model.Impl{}
	undocumentedResults := map[string]model.Undocumented{}
	undocumentedResultBodies := map[string]model.Undocumented{}
	{
		cfg := &packages.Config{
			Mode:  packages.LoadSyntax,
			Dir:   chdir,
			Tests: false,
		}
		pkgs, err := packages.Load(cfg, config.SourceDirectories...)
		if err != nil {
			log.Fatal(err)
		}
		if packages.PrintErrors(pkgs) > 0 {
			panic("package errors")
		}

		// TODO extract the response funcs from the source code: look for functions in the groupware package that return a Response object, and look for the "body" parameter
		responseFuncs := map[string]responseFunc{
			"etagResponse":              {true, 1, http.StatusOK},
			"response":                  {true, 1, http.StatusOK},
			"noContentResponse":         {false, -1, http.StatusNoContent},
			"noContentResponseWithEtag": {false, -1, http.StatusNoContent},
			"notFoundResponse":          {false, -1, http.StatusNotFound},
			"etagNotFoundResponse":      {false, -1, http.StatusNotFound},
			"notImplementedResponse":    {false, -1, http.StatusNotImplemented},
		}

		for _, p := range pkgs {
			if !slices.Contains(config.PackageIDs, p.ID) {
				continue
			}

			for _, name := range p.Types.Scope().Names() {
				obj := p.Types.Scope().Lookup(name)
				switch t := obj.Type().(type) {
				case *types.Signature:
					// skip methods
				case *types.Named:
					name := t.Obj().Name()
					var _ = name
					summary, description := summarizeType(findComments(t.Obj().Pos(), token.TYPE, p))
					if r, err := typeOf(t, summary, description, typeMap, p); err != nil {
						log.Panicf("failed to determine type of named %#v: %v", t, err)
					} else if r != nil {
						typeMap[r.Key()] = r
					}
				case *types.Basic:
					switch t.Kind() {
					case types.UntypedString, types.String:
						constsMap[name] = true
					case types.UntypedInt, types.Int, types.UntypedBool, types.Bool:
						// ignore
					default:
						log.Panicf("> %s is Basic but not string: %s", name, t.Name())
					}
				case *types.Slice:
					//fmt.Printf("globvar(slice): %s: %s\n", name, t.String())
				case *types.Map:
					//fmt.Printf("globvar(map): %s: %s\n", name, t.String())
				case *types.Pointer:
					//fmt.Printf("globvar(ptr): %s: %s\n", name, t.String())
				default:
					log.Panicf("failed to analyze type %s: is not a Named but a %T", name, t)
				}
			}
		}

		// find the Package for "groupware"
		var groupware *packages.Package = nil
		{
			for _, p := range pkgs {
				if p.ID == config.GroupwarePackageID {
					groupware = p
					break
				}
			}
			if groupware == nil {
				panic("failed to find the groupware package " + config.GroupwarePackageID)
			}
		}

		// fill routeFuncs
		{
			for _, d := range groupware.TypesInfo.Defs {
				if d == nil {
					continue
				}
				if f, ok := d.(*types.Func); ok && isRouteFun(f) {
					routeFuncs[groupware.Name+"."+f.Name()] = f
				}
			}
		}

		// analyze routes, route functions and definitions of path and query parameter name
		// constants in groupware_route.go
		{
			var syntax *ast.File = nil
			{
				for i, f := range groupware.CompiledGoFiles {
					if rf, err := filepath.Rel(basepath, f); err != nil {
						panic(err)
					} else if rf == "services/groupware/pkg/groupware/groupware_route.go" {
						syntax = groupware.Syntax[i]
						break
					}
				}
				if syntax == nil {
					panic("failed to find syntax for groupware_route.go")
				}
			}

			routeFunc := findRouteDefinition(syntax)
			if routeFunc == nil {
				log.Fatal("failed to find Route() method")
			}

			{
				if r, err := endpoints("/", routeFunc.Body.List, groupware.Name); err != nil {
					panic(err)
				} else {
					routes = append(routes, r...)
				}
			}
			{
				if g, err := scrapeGlobalParamDecls(syntax.Decls); err != nil {
					panic(err)
				} else {
					maps.Copy(pathParams, g.pathParams)
					maps.Copy(queryParams, g.queryParams)
					maps.Copy(headerParams, g.headerParams)
				}
			}
		}

		for n, f := range routeFuncs {
			if fun, source, ok := findFun(n, groupware, f); !ok {
				log.Panicf("failed to find function declaration for route function '%s' in package '%s'", n, groupware.Name)
			} else {
				comments := []string{}
				if fun.Doc != nil && len(fun.Doc.List) > 0 {
					for _, doc := range fun.Doc.List {
						comments = append(comments, parseComment(doc.Text))
					}
				}

				v := newParamsVisitor(groupware.Fset, groupware, n, typeMap, responseFuncs)
				resp := map[int]model.Resp{}

				if fun.Body != nil {
					ast.Walk(v, fun.Body)
					if err := errors.Join(*v.errs...); err != nil {
						panic(err)
					}
					for code, typename := range v.responseTypes {
						resp[code] = model.Resp{Type: typename}
					}
				}
				source, err := filepath.Rel(basepath, source)
				if err != nil {
					panic(err)
				}
				line := groupware.Fset.Position(fun.Pos()).Line

				var route model.Endpoint
				{
					foundRoute := false
					for _, r := range routes {
						if r.Fun == n {
							route = r
							foundRoute = true
							break
						}
					}
					if !foundRoute {
						log.Panicf("failed to find endpoint for route function '%s'", n)
					}
				}

				tags := tags(route.Verb, route.Path, comments)
				summary, description := summarizeEndpoint(route.Verb, route.Path, comments)

				ims = append(ims, model.Impl{
					Endpoint:     route,
					Name:         n,
					Fun:          fun,
					Source:       source,
					Line:         line,
					Comments:     comments,
					Resp:         resp,
					QueryParams:  valuesOf(v.queryParams),
					PathParams:   valuesOf(v.pathParams),
					HeaderParams: valuesOf(v.headerParams),
					BodyParams:   valuesOf(v.bodyParams),
					Tags:         tags,
					Summary:      summary,
					Description:  description,
				})

				for k, pos := range v.undocumentedResults {
					undocumentedResults[k] = model.Undocumented{
						Pos:      pos,
						Endpoint: route,
					}
				}
				for k, pos := range v.undocumentedRequestBodies {
					undocumentedResultBodies[k] = model.Undocumented{
						Pos:      pos,
						Endpoint: route,
					}
				}
			}
		}
	}

	exampleMap := map[string]model.Examples{}
	{
		m := map[string]map[string]model.Example{}

		for _, d := range config.SourceDirectories {
			p := filepath.Join(chdir, d)
			root := os.DirFS(p)
			if files, err := fs.Glob(root, "example.*.json"); err != nil {
				panic(err)
			} else {
				for _, f := range files {
					k := ""
					q := ""
					n := ""
					if x := exampleFilenameWithPackageRegex.FindAllStringSubmatch(f, 3); x != nil {
						q = x[0][1]                 // qualifier
						k = x[0][2] + "." + x[0][3] // fully qualified type name (package . name)
						n = x[0][3]                 // just the type name without the package
					} else {
						panic(fmt.Errorf("%s: example filename does not match the required pattern '%s': '%s'", p, exampleFilenameWithPackageRegex, f))
					}

					qf := filepath.Join(chdir, d, f)
					if b, err := os.ReadFile(qf); err != nil {
						panic(err)
					} else {
						title := ""
						text := ""
						{
							lines := strings.Split(string(b), "\n")
							if len(lines) > 0 && strings.HasPrefix(lines[0], "#") {
								title = strings.TrimSpace(strings.TrimPrefix(lines[0], "#"))
								text = strings.Join(lines[1:], "\n")
							} else {
								text = string(b)
							}
						}

						if title == "" {
							v := ""
							if voweled(n) {
								v = "n"
							}
							title = fmt.Sprintf("A%s %s", v, n)
						}

						if _, ok := m[k]; !ok {
							m[k] = map[string]model.Example{}
						}
						m[k][q] = model.Example{
							Key:    k + ":" + q,
							Title:  title,
							Text:   text,
							Origin: filepath.Join(d, f),
						}
					}
				}
			}
		}

		for k, qualified := range m {
			e := model.Examples{
				Key: k,
			}
			if x, ok := qualified["any"]; ok {
				e.DefaultExample = x
			} else {
				panic(fmt.Errorf("no default example for %s", k))
			}
			if x, ok := qualified["request"]; ok {
				e.RequestExample = &x
			}
			remaining := maps.Clone(qualified)
			delete(remaining, "any")
			delete(remaining, "request")
			if len(remaining) > 0 {
				panic(fmt.Errorf("unsupported qualifiers found for examples for '%s': %s", k, strings.Join(slices.Collect(maps.Keys(remaining)), ", ")))
			}
			exampleMap[k] = e
		}
	}

	enums := map[string][]string{} // TODO

	defaultResponses := map[int]model.Type{}
	{
		if t, ok := typeMap["groupware.ErrorResponse"]; ok {
			for _, statusCode := range []int{400, 404, 500} {
				defaultResponses[statusCode] = t
			}
		}
	}

	// TODO extract default headers and their documentation from the source code (groupware_framework.go)
	defaultResponseHeaders := map[string]model.ResponseHeaderDesc{} // TODO use a struct instead of string to indicate more information (required or not)
	{
		// defaultHeaders["Content-Language"]
		// defaultHeaders["ETag"]
		defaultResponseHeaders["Session-State"] = model.ResponseHeaderDesc{Summary: "The opaque state identifier for the JMAP Session", Required: true}
		defaultResponseHeaders["State"] = model.ResponseHeaderDesc{Summary: "The opaque state identifier for the type of objects in the response"}
		defaultResponseHeaders["Object-Type"] = model.ResponseHeaderDesc{Summary: "The type of JMAP objects returned in the response"}
		defaultResponseHeaders["Account-Id"] = model.ResponseHeaderDesc{Summary: "The identifier of the account the operation was performed against, when against a single account"}
		defaultResponseHeaders["Account-Ids"] = model.ResponseHeaderDesc{Summary: "The identifier of the accounts the operation was performed against, when against multiple accounts", Explode: true}
		defaultResponseHeaders["Trace-Id"] = model.ResponseHeaderDesc{Summary: "The value of the Trace-Id header that was specified in the request or, if not, a unique randomly generated identifier that is included in logging output", Required: true}
	}

	commonRequestHeaders := []model.RequestHeaderDesc{}
	{
		commonRequestHeaders = append(commonRequestHeaders, model.RequestHeaderDesc{
			Name:        "X-Request-ID",
			Description: "When specified, its value is used in logs for correlation",
		})
		commonRequestHeaders = append(commonRequestHeaders, model.RequestHeaderDesc{
			Name:        "Trace-Id",
			Description: "When specified, its value is used in logs for correlation and if not, a new random value is generated and sent in the response",
		})
	}

	types := slices.Collect(maps.Values(typeMap))

	return model.Model{
		Routes:                    routes,
		PathParams:                pathParams,
		QueryParams:               queryParams,
		HeaderParams:              headerParams,
		Impls:                     ims,
		Types:                     types,
		Examples:                  exampleMap,
		Enums:                     enums,
		DefaultResponses:          defaultResponses,
		DefaultResponseHeaders:    defaultResponseHeaders,
		CommonRequestHeaders:      commonRequestHeaders,
		UndocumentedResults:       undocumentedResults,
		UndocumentedRequestBodies: undocumentedResultBodies,
	}, nil
}

func typeOf(t types.Type, summary string, description string, mem map[string]model.Type, p *packages.Package) (model.Type, error) {
	switch t := t.(type) {
	case *types.Named:
		name := t.Obj().Name()
		pkg := ""
		if t.Obj().Pkg() != nil {
			pkg = t.Obj().Pkg().Name()
			if model.IsBuiltinSelectorType(pkg, name) { // for things like time.Time
				return model.NewBuiltinType(pkg, name), nil
			}
		} else {
			if model.IsBuiltinType(name) {
				return model.NewBuiltinType("", name), nil
			}
		}

		pos := token.Position{}
		{
			tp := token.NoPos
			if t.Obj() != nil {
				tp = t.Obj().Pos()
			}
			if tp != token.NoPos {
				pos = p.Fset.Position(tp)
			}
		}

		switch u := t.Underlying().(type) {
		case *types.Basic:
			return model.NewAliasType(pkg, name, model.NewBuiltinType("", u.Name()), pos), nil
		case *types.Interface:
			return model.NewInterfaceType(pkg, name, pos), nil
		case *types.Map:
			return mapOf(u, mem, p)
		case *types.Array:
			return arrayOf(u.Elem(), summary, description, mem, p)
		case *types.Slice:
			return arrayOf(u.Elem(), summary, description, mem, p)
		case *types.Pointer:
			return typeOf(u.Elem(), summary, description, mem, p) // TODO pointer denotes that it's optional
		case *types.Struct:
			id := fmt.Sprintf("%s.%s", pkg, name)
			if ex, ok := mem[id]; ok {
				return ex, nil
			}

			structFields, err := decompose(name, u, 0, p)
			if err != nil {
				return nil, err
			}

			fields := []model.Field{}
			// add the struct with an empty list of fields into the memory (mem) to avoid
			// endless looping when attempting to resolve the type using typeOf(), in case
			// of circular references
			r := model.NewStructType(pkg, name, fields, summary, description, pos)
			mem[id] = r
			{
				for _, f := range structFields {
					fieldSummary, fieldDescription := summarizeType(findComments(f.pos, token.VAR, p))
					if typ, err := typeOf(f.typ, fieldSummary, fieldDescription, mem, p); err != nil {
						return nil, err
					} else {
						if field, ok := model.NewField(f.name, typ, f.tag, fieldSummary); ok { // TODO what about fieldDescription?
							fields = append(fields, field)
						}
					}
				}
			}
			// and now overwrite the struct type with a definition that contains the fields
			r = model.NewStructType(pkg, name, fields, summary, description, pos)
			mem[id] = r
			return r, nil
		default:
			return nil, fmt.Errorf("typeOf: unsupported underlying type of named %s.%s is a %T: %#v", pkg, name, u, u)
		}
	case *types.Basic:
		return model.NewBuiltinType("", t.Name()), nil
	case *types.Map:
		return mapOf(t, mem, p)
	case *types.Array:
		return arrayOf(t.Elem(), summary, description, mem, p)
	case *types.Slice:
		return arrayOf(t.Elem(), summary, description, mem, p)
	case *types.Pointer:
		return typeOf(t.Elem(), summary, description, mem, p)
	case *types.Alias:
		return aliasOf(t, mem, p)
	case *types.Interface:
		if t.String() == "any" {
			return model.NewBuiltinType("", "any"), nil
		} else {
			return nil, fmt.Errorf("typeOf: unsupported: using an interface type that isn't any: %T: %#v", t, t)
		}
	case *types.TypeParam:
		// ignore
		return nil, nil
	case *types.Chan:
		// ignore
		return nil, nil
	case *types.Struct:
		// ignore unnamed struct
		return nil, nil
	default:
		return nil, fmt.Errorf("typeOf: unsupported type: %T: %#v", t, t)
	}
}

type field struct {
	name string
	tag  string
	typ  types.Type
	pos  token.Pos
}

func decompose(name string, u *types.Struct, level int, p *packages.Package) ([]field, error) {
	if level > 10 {
		log.Panicf("recursing level %d", level)
	}
	structFields := []field{}
	for i := range u.NumFields() {
		f := u.Field(i)
		switch f.Type().Underlying().(type) {
		case *types.Signature:
			// ignore
		default:
			tag := u.Tag(i)
			if f.Embedded() {
				var c *types.Struct = nil
				switch t := f.Type().(type) {
				case *types.Named:
					switch ut := t.Underlying().(type) {
					case *types.Struct:
						c = ut
					case *types.Interface:
						// ignore
						continue
						// return nil, fmt.Errorf("found an interface")
					default:
						return nil, fmt.Errorf("while typeOf('%s'): (underlying) embedded field is not a struct but a %T", name, ut)
					}
				case *types.Struct:
					c = t
				default:
					return nil, fmt.Errorf("while typeOf('%s'): embedded field is not a struct but a %T", name, u)
				}
				if c == nil {
					return nil, fmt.Errorf("while typeOf('%s'): embedded field is not a struct but a %T", name, u)
				} else {
					if sub, err := decompose(name, c, level+1, p); err != nil { // this could cause an infinite loop
						return nil, err
					} else {
						structFields = append(structFields, sub...)
					}
				}
			} else {
				structFields = append(structFields, field{
					name: f.Name(),
					typ:  f.Type(),
					pos:  f.Pos(),
					tag:  tag,
				})
			}
		}
	}
	return structFields, nil
}

func arrayOf(t types.Type, summary string, description string, mem map[string]model.Type, p *packages.Package) (model.Type, error) {
	if e, err := typeOf(t, summary, description, mem, p); err != nil {
		return nil, err
	} else {
		return model.NewArrayType(e), nil
	}
}

func mapOf(t *types.Map, mem map[string]model.Type, p *packages.Package) (model.Type, error) {
	if k, err := typeOf(t.Key(), "", "", mem, p); err != nil {
		return nil, err
	} else {
		if v, err := typeOf(t.Elem(), "", "", mem, p); err != nil {
			return nil, err
		} else {
			return model.NewMapType(k, v), nil
		}
	}
}

func aliasOf(t *types.Alias, mem map[string]model.Type, p *packages.Package) (model.Type, error) {
	if e, err := typeOf(t.Underlying(), "", "", mem, p); err != nil {
		return nil, err
	} else {
		pkg := ""
		if t.Obj().Pkg() != nil {
			pkg = t.Obj().Pkg().Name()
		}
		return model.NewAliasType(pkg, t.Obj().Name(), e, p.Fset.Position(t.Obj().Pos())), nil
	}
}
