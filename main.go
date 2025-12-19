package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
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
)

var packagesOfInterest = []string{
	"groupware",
	"jmap",
	"jscontact",
	"jscalendar",
}

const (
	GroupwarePackageID  = "github.com/opencloud-eu/opencloud/services/groupware/pkg/groupware"
	JmapPackageID       = "github.com/opencloud-eu/opencloud/pkg/jmap"
	JSCalendarPackageID = "github.com/opencloud-eu/opencloud/pkg/jscalendar"
	JSContactPacakgeID  = "github.com/opencloud-eu/opencloud/pkg/jscontact"
)

var PackageIDs = []string{
	GroupwarePackageID,
	JmapPackageID,
	JSCalendarPackageID,
	JSContactPacakgeID,
}

func pickRoute(a *ast.File) *ast.FuncDecl {
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

type Endpoint struct {
	Verb string
	Path string
	Fun  string
}

type Resp struct {
	Type Type
}

type Impl struct {
	Source      string
	Fun         *ast.FuncDecl
	Name        string
	Comments    []string
	Resp        map[int]Resp
	QueryParams []string
	UriParams   []string
	BodyParams  []string
}

var verbs = []string{
	"Get",
	"Put",
	"Post",
	"Delete",
	"Patch",
}

var customVerbs = []string{
	"Report",
}

func endpoints(base string, stmts []ast.Stmt, pkg string) []Endpoint {
	result := []Endpoint{}
	for _, stmt := range stmts {
		if call := stmtIsCallExpr(stmt); call != nil {
			if verb := methodNameOf(call, "r"); verb != "" {
				if verb == "Route" {
					// recurse
					if path := stringArgOf(call, 0); path != "" {
						if deep := funcArgOf(call, 1); deep != nil {
							result = append(result, endpoints(join(base, path), deep.Body.List, pkg)...)
						}
					}
				} else if slices.Contains(verbs, verb) && len(call.Args) == 2 {
					if path := stringArgOf(call, 0); path != "" {
						if f := funcRefArgOf(call, 1); f != nil {
							fun := nameOf(f.Sel, pkg)
							result = append(result, Endpoint{Verb: strings.ToUpper(verb), Path: join(base, path), Fun: fun})
						}
					}
				} else if slices.Contains(customVerbs, verb) && len(call.Args) == 3 {
					if path := stringArgOf(call, 1); path != "" {
						if f := funcRefArgOf(call, 2); f != nil {
							fun := nameOf(f.Sel, pkg)
							result = append(result, Endpoint{Verb: strings.ToUpper(verb), Path: join(base, path), Fun: fun})
						}
					}
				}
			}
		}
	}
	return result
}

func params(decls []ast.Decl) (map[string]string, map[string]string) {
	uriParams := map[string]string{}
	queryParams := map[string]string{}
	for _, decl := range decls {
		for n, v := range isConstDecl(decl) {
			if strings.HasPrefix(n, "UriParam") {
				uriParams[n] = v
			} else if strings.HasPrefix(n, "QueryParam") {
				queryParams[n] = v
			}
		}
	}
	return uriParams, queryParams
}

type responseFunc struct {
	hasBody    bool
	bodyArgPos int
	statusCode int
}

type returnVisitor struct {
	fset          *token.FileSet
	typeMap       map[string]Type
	pkg           *packages.Package
	responseTypes map[int]Type
	responseFuncs map[string]responseFunc
}

func newReturnVisitor(fset *token.FileSet, pkg *packages.Package, typeMap map[string]Type, responseFuncs map[string]responseFunc) returnVisitor {
	return returnVisitor{
		fset:          fset,
		pkg:           pkg,
		typeMap:       typeMap,
		responseTypes: map[int]Type{},
		responseFuncs: responseFuncs,
	}
}

func keyOf(n *types.Named) string {
	key := n.Obj().Name()
	if n.Obj().Pkg() != nil {
		key = n.Obj().Pkg().Name() + "." + key
	}
	return key
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

func (v returnVisitor) Visit(n ast.Node) ast.Visitor {
	if n == nil { // = nothing left to visit
		return nil
	}
	r, ok := n.(*ast.ReturnStmt)
	if !ok {
		return v
	}
	if a, code, ok := v.isResponseFunc(r); ok {
		if a != nil {
			switch x := a.(type) {
			case *ast.SelectorExpr:
				key := x.X.(*ast.Ident).Name + "." + x.Sel.Name
				if m, ok := v.typeMap[key]; ok {
					v.responseTypes[code] = m
				} else {
					keys := slices.Collect(maps.Keys(v.typeMap))
					fmt.Printf("typemap has the following types:\n")
					for _, k := range keys {
						fmt.Printf("  - %s\n", k)
					}
					panic(fmt.Sprintf("failed to find the type '%s' in the typeMap", key))
				}
			case *ast.Ident:
				if x.Obj == nil {
					panic(fmt.Sprintf("response body argument is an ident that has a nil Obj: %v", x))
				}
				switch d := x.Obj.Decl.(type) {
				case *ast.AssignStmt:
					// ignore
				case *ast.ValueSpec:
					if len(d.Names) == 1 {
						if t, ok := isIdent(d.Names[0]); ok && strings.HasPrefix(t.Name, "RBODY") {
							suffix := t.Name[5:]
							if suffix != "" {
								var err error
								code, err = strconv.Atoi(suffix)
								if err != nil {
									panic(fmt.Sprintf("failed to parse integer status code in variable name '%v': %s\n", t.Name, err))
								}
							}

							key := ""
							{
								z := v.pkg.TypesInfo.Uses[x]
								switch n := z.Type().(type) {
								case *types.Named:
									key = keyOf(n)
								case *types.Slice:
									if nn, ok := n.Elem().(*types.Named); ok {
										key = keyOf(nn)
									} else {
										panic(fmt.Sprintf("unsupported slice elem: %T: %#v", n.Elem(), n.Elem()))
									}
								default:
									panic(fmt.Sprintf("unsupported z: %T: %#v", z.Type(), z.Type()))
								}
							}
							if m, ok := v.typeMap[key]; ok {
								v.responseTypes[code] = m
							} else {
								panic(fmt.Sprintf("(x) not found: %v", key))
							}
						} else {
							panic("it's not RBODY")
						}
					} else {
						panic("d.Names len is not == 1")
					}
				default:
					ast.Print(v.fset, d)
					panic(fmt.Sprintf("unsupported return decl: %T %v", d, d))
				}
			}
		} else {
			// if a is nil, it means that there is no body
			v.responseTypes[code] = nil
		}
	}
	return v
}

type paramsVisitor struct {
	fset          *token.FileSet
	pkg           *packages.Package
	typeMap       map[string]Type
	queryParams   map[string]bool
	uriParams     map[string]bool
	bodyParams    map[string]bool
	responseTypes map[int]Type
	responseFuncs map[string]responseFunc
}

func newParamsVisitor(fset *token.FileSet, pkg *packages.Package, typeMap map[string]Type, responseFuncs map[string]responseFunc) paramsVisitor {
	return paramsVisitor{
		fset:          fset,
		pkg:           pkg,
		typeMap:       typeMap,
		queryParams:   map[string]bool{},
		uriParams:     map[string]bool{},
		bodyParams:    map[string]bool{},
		responseTypes: map[int]Type{},
		responseFuncs: responseFuncs,
	}
}

func isValueSpec(expr any) (*ast.ValueSpec, bool) {
	switch e := expr.(type) {
	case *ast.ValueSpec:
		return e, true
	}
	return nil, false
}

func (v paramsVisitor) isBodyCall(call *ast.CallExpr) (string, bool) {
	if s, ok := isSelector(call.Fun); ok {
		if x, ok := isIdent(s.X); ok && s.Sel.Name == "body" && len(call.Args) == 1 && x.Obj != nil {
			if f, ok := isField(x.Obj.Decl); ok {
				if t, ok := isIdent(f.Type); ok && t.Name == "Request" {
					a := call.Args[0]
					switch e := a.(type) {
					case *ast.UnaryExpr:
						if x, ok := isIdent(e.X); ok && e.Op == token.AND && x.Obj != nil {
							if vs, ok := isValueSpec(x.Obj.Decl); ok {
								n := nameOf(vs.Type, v.pkg.Name)
								return n, true
							} else {
								log.Fatalf("unsupported call to Request.body(): UnaryExpr argument is not an Ident but a %v", e)
							}
						}
					default:
						log.Fatalf("unsupported call to Request.body(): is not a UnaryExpr but a %v", e)
					}
				} else {
					log.Fatalf("call to body() but not on a Request: %v", f)
				}
			} else {
				log.Fatalf("call to body() but is not a field: %v", x.Obj)
			}
		}
	}
	return "", false
}

func isClosure(expr ast.Expr) (*ast.FuncLit, bool) {
	switch e := expr.(type) {
	case *ast.FuncLit:
		return e, true
	}
	return nil, false
}

func (v paramsVisitor) isRespondCall(call *ast.CallExpr) (map[int]Type, bool) {
	if s, ok := isSelector(call.Fun); ok {
		if x, ok := isIdent(s.X); ok && s.Sel.Name == "respond" && len(call.Args) == 3 && x.Obj != nil {
			if f, ok := isField(x.Obj.Decl); ok {
				if t, ok := isIdent(f.Type); ok && t.Name == "Groupware" {
					a := call.Args[2]
					if c, ok := isClosure(a); ok {
						r := newReturnVisitor(v.fset, v.pkg, v.typeMap, v.responseFuncs)
						ast.Walk(r, c)
						if len(r.responseTypes) > 0 {
							return r.responseTypes, true
						}
					}
				}
			}
		}
	}
	return nil, false
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

var need regexp.Regexp = *regexp.MustCompile(`^need(Contact|Calendar|Task)WithAccount$`)

func (v paramsVisitor) isNeedAccountCall(call *ast.CallExpr) (string, bool) {
	if s, ok := isSelector(call.Fun); ok {
		if x, ok := isIdent(s.X); ok && len(call.Args) == 0 && x.Obj != nil {
			if m := need.FindAllStringSubmatch(s.Sel.Name, 2); m != nil {
				if f, ok := isField(x.Obj.Decl); ok {
					if t, ok := isIdent(f.Type); ok && t.Name == "Request" {
						return "UriParamAccountId", true
					}
				}
			}
		}
	}
	return "", false
}

func (v paramsVisitor) Visit(n ast.Node) ast.Visitor {
	if n == nil { // = nothing left to visit
		return nil
	}
	switch d := n.(type) {
	case *ast.Ident:
		if strings.HasPrefix(d.Name, "QueryParam") {
			v.queryParams[d.Name] = true
		} else if strings.HasPrefix(d.Name, "UriParam") {
			v.uriParams[d.Name] = true
		}
	case *ast.CallExpr:
		if t, ok := v.isBodyCall(d); ok {
			v.bodyParams[t] = true
		} else if v.isAccountCall(d) {
			v.uriParams["UriParamAccountId"] = true
		} else if t, ok := v.isRespondCall(d); ok {
			maps.Copy(v.responseTypes, t)
		} else if z, ok := v.isNeedAccountCall(d); ok {
			v.uriParams[z] = true
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

type Model struct {
	Routes           []Endpoint
	UriParams        map[string]string
	QueryParams      map[string]string
	Impls            []Impl
	Types            []Type
	Enums            map[string][]string
	DefaultResponses map[int]Type
}

func (m Model) resolveType(k string) (Type, bool) {
	for _, t := range m.Types {
		if t.Key() == k {
			return t, true
		}
	}
	return nil, false
}

type Sink interface {
	Output(model Model)
}

var verbose bool = false

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

func findFun(n string, p *packages.Package, fun *types.Func) (*ast.FuncDecl, string, bool) {
	for i, f := range p.CompiledGoFiles {
		s := p.Syntax[i] // TODO doesn't necessarily fit, can have nils that are compacted away
		for _, d := range s.Decls {
			switch x := d.(type) {
			case *ast.FuncDecl:
				fname := s.Name.Name + "." + x.Name.Name
				if n == fname {
					return x, f, true
				}
				if fun.Pos() <= x.Pos() && x.End() >= fun.Pos() {
					return x, f, true
				}
			}
		}
	}
	return nil, "", false
}

func main() {
	chdir := ""
	flag.StringVar(&chdir, "C", "", "Change into the specified directory before parsing source files")
	flag.BoolVar(&verbose, "v", false, "Output verbose information while parsing source files")
	flag.Parse()

	var err error
	basepath := ""
	if chdir != "" {
		basepath, err = filepath.Abs(chdir)
		if err != nil {
			log.Fatalf("failed to make an absolute path out of '%s': %v", chdir, err)
		}
	} else {
		basepath, err = os.Getwd()
		if err != nil {
			log.Fatalf("failed to get current working directory: %v", err)
		}
		basepath, err = filepath.Abs(basepath)
		if err != nil {
			log.Fatalf("failed to make an absolute path out of the current working directory '%s': %v", chdir, err)
		}
	}

	routeFuncs := map[string]*types.Func{}
	typeMap := map[string]Type{}
	constsMap := map[string]bool{}
	var routes []Endpoint
	var uriParams map[string]string
	var queryParams map[string]string
	ims := []Impl{}
	{
		cfg := &packages.Config{
			Mode:  packages.LoadSyntax,
			Dir:   chdir,
			Tests: false,
		}
		pkgs, err := packages.Load(cfg,
			"./services/groupware/pkg/groupware",
			"./pkg/jmap",
			"./pkg/jscontact",
			"./pkg/jscalendar",
		)
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
			if !slices.Contains(PackageIDs, p.ID) {
				continue
			}

			for _, name := range p.Types.Scope().Names() {
				obj := p.Types.Scope().Lookup(name)
				switch t := obj.Type().(type) {
				case *types.Signature:
					// skip methods
				case *types.Named:
					r := typeOf(t, typeMap)
					if r != nil {
						typeMap[r.Key()] = r
					}
				case *types.Basic:
					switch t.Kind() {
					case types.UntypedString, types.String:
						constsMap[name] = true
					case types.UntypedInt, types.Int, types.UntypedBool, types.Bool:
						// ignore
					default:
						panic(fmt.Sprintf("> %s is Basic but not string: %s\n", name, t.Name()))
					}
				case *types.Slice:
					//fmt.Printf("globvar(slice): %s: %s\n", name, t.String())
				case *types.Map:
					//fmt.Printf("globvar(map): %s: %s\n", name, t.String())
				case *types.Pointer:
					//fmt.Printf("globvar(ptr): %s: %s\n", name, t.String())
				default:
					panic(fmt.Sprintf("> %s is not Named but %T\n", name, t))
				}
			}
		}

		for _, p := range pkgs {
			if p.ID != GroupwarePackageID {
				continue
			}
			for _, d := range p.TypesInfo.Defs {
				if d == nil {
					continue
				}
				if f, ok := d.(*types.Func); ok && isRouteFun(f) {
					routeFuncs[p.Name+"."+f.Name()] = f
				}
			}

			var syntax *ast.File = nil
			for i, f := range p.CompiledGoFiles {
				rf, err := filepath.Rel(basepath, f)
				if err != nil {
					panic(err)
				}
				if rf == "services/groupware/pkg/groupware/groupware_route.go" {
					syntax = p.Syntax[i]
				}
			}
			if syntax != nil {
				route := pickRoute(syntax)
				if route == nil {
					log.Fatal("failed to find Route() method")
				}

				routes = endpoints("/", route.Body.List, p.Name)
				uriParams, queryParams = params(syntax.Decls)

				for n, f := range routeFuncs {
					if fun, source, ok := findFun(n, p, f); ok {
						comments := []string{}
						if fun.Doc != nil && len(fun.Doc.List) > 0 {
							for _, doc := range fun.Doc.List {
								comments = append(comments, parseComment(doc.Text))
							}
						}

						resp := map[int]Resp{}
						pv := newParamsVisitor(p.Fset, p, typeMap, responseFuncs)
						if fun.Body != nil {
							ast.Walk(pv, fun.Body)
							for code, typename := range pv.responseTypes {
								resp[code] = Resp{Type: typename}
							}
						}
						source, err := filepath.Rel(basepath, source)
						if err != nil {
							panic(err)
						}

						ims = append(ims, Impl{
							Name:        n,
							Fun:         fun,
							Source:      source,
							Comments:    comments,
							Resp:        resp,
							QueryParams: keysOf(pv.queryParams),
							UriParams:   keysOf(pv.uriParams),
							BodyParams:  keysOf(pv.bodyParams),
						})
					} else {
						panic(fmt.Sprintf("failed to find syntax for route function %s\n", n))
					}
				}
			} else {
				panic("failed to find syntax for groupware_route.go")
			}
		}
	}

	enums := map[string][]string{} // TODO

	defaultResponses := map[int]Type{}
	{
		if t, ok := typeMap["groupware.ErrorResponse"]; ok {
			for _, statusCode := range []int{400, 404, 500} {
				defaultResponses[statusCode] = t
			}
		}
	}

	types := slices.Collect(maps.Values(typeMap))

	model := Model{
		Routes:           routes,
		UriParams:        uriParams,
		QueryParams:      queryParams,
		Impls:            ims,
		Types:            types,
		Enums:            enums,
		DefaultResponses: defaultResponses,
	}

	o := AnsiSink{}
	o.Output(model)
}
