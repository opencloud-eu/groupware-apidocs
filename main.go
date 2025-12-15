package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log"
	"maps"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

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
	Fun         string
	Comments    []string
	Resp        map[int]Resp
	QueryParams []string
	UriParams   []string
	BodyParams  []string
}

type Field struct {
	Pkg  string
	Name string
	Type Type
	Tag  string
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

// api:response:200: *jscontact.ContactCard
// var apiResponseRegex = regexp.MustCompile(`^//\s*api:response:(\d+):\s*(\S+)$`)

func isGroupwareRouteFunc(d *ast.FuncDecl) bool {
	return isMemberOf(d, "Groupware") && hasNumParams(d, 2) && isParamType(d.Type.Params.List[0], "http", "ResponseWriter") && isParamPtrType(d.Type.Params.List[1], "http", "Request")
}

type returnVisitor struct {
	fset          *token.FileSet
	pkg           string
	responseTypes map[int]Type
}

func newReturnVisitor(fset *token.FileSet, pkg string) returnVisitor {
	return returnVisitor{
		fset:          fset,
		pkg:           pkg,
		responseTypes: map[int]Type{},
	}
}

func (v returnVisitor) Visit(n ast.Node) ast.Visitor {
	if n == nil { // = nothing left to visit
		return nil
	}
	switch r := n.(type) {
	case *ast.ReturnStmt:
		if len(r.Results) == 1 {
			if c, ok := isCallExpr(r.Results[0]); ok {
				if i, ok := isIdent(c.Fun); ok && i.Name == "etagResponse" && len(c.Args) >= 2 {
					if j, ok := isIdent(c.Args[1]); ok && j.Obj != nil {
						switch d := j.Obj.Decl.(type) {
						case *ast.AssignStmt:
							// ignore
						case *ast.ValueSpec:
							if len(d.Names) == 1 {
								if t, ok := isIdent(d.Names[0]); ok && strings.HasPrefix(t.Name, "RBODY") {
									suffix := t.Name[5:]
									if suffix == "" {
										v.responseTypes[200] = typeRef(d.Type, v.pkg)
									} else {
										code, err := strconv.Atoi(suffix)
										if err != nil {
											log.Fatalf("failed to parse integer status code in variable name '%v': %s", t.Name, err)
										}
										v.responseTypes[code] = typeRef(d.Type, v.pkg)
									}
								}
							}
						default:
							ast.Print(v.fset, d)
							panic(fmt.Sprintf("unsupported return decl: %T %v", d, d))
						}
					}
				}
			}
		}
	}

	return v
}

type paramsVisitor struct {
	fset          *token.FileSet
	pkg           string
	queryParams   map[string]bool
	uriParams     map[string]bool
	bodyParams    map[string]bool
	responseTypes map[int]Type
}

func newParamsVisitor(fset *token.FileSet, pkg string) paramsVisitor {
	return paramsVisitor{
		fset:          fset,
		pkg:           pkg,
		queryParams:   map[string]bool{},
		uriParams:     map[string]bool{},
		bodyParams:    map[string]bool{},
		responseTypes: map[int]Type{},
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
								n := nameOf(vs.Type, v.pkg)
								return n, true
							}
						}
					}
				}
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
						r := newReturnVisitor(v.fset, v.pkg)
						ast.Walk(r, c)
						if len(r.responseTypes) > 0 {
							return r.responseTypes, true
						}
					}
					// fmt.Printf("### RESPOND: ")
					// ast.Print(v.fset, a)
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

func impls(fset *token.FileSet, a *ast.File, source string) ([]Impl, []Type) {
	//fmt.Printf("[📃] \x1b[34;4;1m%s\x1b[0m\n", source)
	ims := []Impl{}
	types := []Type{}
	//enums := map[string]map[string]string{}
	pkg := ""

	if p, ok := isIdent(a.Name); ok {
		pkg = p.Name
	} else {
		log.Fatalf("no package name found")
	}

	for _, decl := range a.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if isGroupwareRouteFunc(d) {
				fun := nameOf(d.Name, pkg)
				//fmt.Printf("\x1b[4;1;34m%s\x1b[0m\n", fun)
				comments := []string{}
				resp := map[int]Resp{}
				/*
					if d.Doc != nil && d.Doc.List != nil {
						for _, doc := range d.Doc.List {
							comment := strings.TrimSpace(doc.Text)
							if match := apiResponseRegex.FindAllStringSubmatch(comment, -1); len(match) > 0 {
								statusCode, _ := strconv.Atoi(match[0][1]) // TODO err
								ret := TypeRef{Name: match[0][2]}
								resp[statusCode] = Resp{Type: ret}
							}
							comments = append(comments, doc.Text)
						}
					}
				*/
				if d.Doc != nil && d.Doc.List != nil {
					for _, doc := range d.Doc.List {
						comments = append(comments, parseComment(doc.Text))
					}
				}

				v := newParamsVisitor(fset, pkg)
				if d.Body != nil {
					//ast.Print(fset, d.Body)
					ast.Walk(v, d.Body)
				}
				for code, typename := range v.responseTypes {
					resp[code] = Resp{Type: typename}
				}
				ims = append(ims, Impl{
					Source:      source,
					Fun:         fun,
					Comments:    comments,
					Resp:        resp,
					QueryParams: keysOf(v.queryParams),
					UriParams:   keysOf(v.uriParams),
					BodyParams:  keysOf(v.bodyParams),
				})
				//fmt.Printf("\n")
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch v := spec.(type) {
				case *ast.TypeSpec:
					tname := v.Name.Name

					switch t := v.Type.(type) {
					case *ast.StructType:
						if strings.HasPrefix(tname, "Swagger") {
							continue
						}
						if unicode.IsLower(rune(tname[0])) {
							continue
						}
						//fmt.Printf("  - [🚧] \x1b[35;1m%s\x1b[0m\n", v.Name)
						fields := []Field{}
						if t.Fields != nil && t.Fields.List != nil {
							for _, f := range t.Fields.List {
								if f != nil && f.Tag != nil {
									//spew.Dump(f)
									fields = append(fields, Field{
										Pkg:  pkg,
										Name: typeName(f.Names),
										Type: typeRef(f.Type, pkg),
										Tag:  tagOf(f),
									})
								}
							}
						}
						ty := newCustomType(pkg, tname)
						ty.fields = fields
						types = append(types, ty)
					case *ast.InterfaceType:
						// noop
					default:
						tr := newAliasType(pkg, tname, typeRef(v.Type, pkg))
						//fmt.Printf("typespec: %s.%s=%v -> %#v\n", pkg, tname, v.Type, tr)
						types = append(types, tr)
					}
				}
			}
		}
	}
	return ims, types
}

func hi(path string) string {
	parts := strings.Split(path, "/")
	result := make([]string, len(parts))
	for i, part := range parts {
		if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") {
			result[i] = "\x1b[35;1m" + part + "\x1b[0m"
		} else {
			result[i] = part
		}
	}
	return strings.Join(result, "/")
}

func parse(filename string) (*token.FileSet, *ast.File, error) {
	fset := token.NewFileSet()
	src, err := os.ReadFile(filename)
	if err != nil {
		return nil, nil, err
	}
	a, err := parser.ParseFile(fset, filename, src, parser.ParseComments+parser.AllErrors)
	if err != nil {
		return nil, nil, err
	}
	return fset, a, nil
}

var tagRegex = regexp.MustCompile(`json:"(.+?)(?:,(omitempty|omitzero))?"`)

func fieldName(field Field) string {
	if field.Tag == "" {
		return field.Pkg + "." + field.Name
	}
	if m := tagRegex.FindAllStringSubmatch(field.Tag, 2); m != nil {
		return m[0][1]
	} else {
		log.Fatalf("failed to parse tag '%v'", field.Tag)
		return ""
	}
}

func fieldType(field Field) string {
	return field.Type.String()
}

func printType(t Type, m map[string]Type, e map[string]map[string]bool, l int, p string) {
	clr := fmt.Sprintf("\x1b[%dm", 31+l)
	pp := p + strings.Repeat("  ", l)

	if len(t.Fields()) > 0 {
		fmt.Printf("%s %s{\x1b[0m\n", t.String(), clr)
		for _, f := range t.Fields() {
			fmt.Printf("%s %s-\x1b[0m ", pp, clr)
			if !strings.Contains(f.Type.String(), ".") {
				fmt.Printf("\x1b[4m%s\x1b[0m %s\n", fieldName(f), fieldType(f))
			} else {
				fmt.Printf("\x1b[4m%s\x1b[0m ", fieldName(f))
				printType(f.Type, m, e, l+1, p)
			}
		}
		fmt.Printf("%s %s}\x1b[0m\n", pp, clr)
	} else {
		switch v := t.(type) {
		case AliasType:
			fmt.Printf("%s (%s)", t.Key(), v.typeRef.String())
			if n, ok := e[t.Key()]; ok {
				fmt.Printf(" [%s]", strings.Join(slices.Collect(maps.Keys(n)), ","))
			}
			fmt.Printf("\n")
		default:
			fmt.Printf("%s\n", t.String())
		}
	}
}

func main() {
	if len(os.Args) == 2 {
		err := os.Chdir(os.Args[1])
		if err != nil {
			log.Fatal(err)
		}
	}

	var routes []Endpoint
	var uriParams map[string]string
	var queryParams map[string]string
	{
		_, a, err := parse("services/groupware/pkg/groupware/groupware_route.go")
		if err != nil {
			log.Fatal(err)
		}

		pkg := ""
		if i, ok := isIdent(a.Name); ok {
			pkg = i.Name
		} else {
			log.Fatalf("failed to determine package name of groupware_route.go")
		}

		route := pickRoute(a)
		if route == nil {
			log.Fatal("failed to find Route() method")
		}

		routes = endpoints("/", route.Body.List, pkg)
		uriParams, queryParams = params(a.Decls)
	}

	// load everything else and match the functions
	ims := []Impl{}
	types := []Type{}
	{
		sources, err := os.ReadDir("services/groupware/pkg/groupware")
		if err != nil {
			log.Fatal(err)
		}
		for _, source := range sources {
			filename := source.Name()
			if !(strings.HasPrefix(filename, "groupware_") && strings.HasSuffix(filename, "go")) {
				continue
			}

			fset, b, err := parse(filepath.Join("services/groupware/pkg/groupware", filename))
			if err != nil {
				log.Fatal(err)
			}

			sourceIms, sourceTypes := impls(fset, b, filename)
			ims = append(ims, sourceIms...)
			types = append(types, sourceTypes...)
		}
	}

	{
		for _, d := range []string{"pkg/jmap", "pkg/jscontact", "pkg/jscalendar"} {
			sources, err := os.ReadDir(d)
			if err != nil {
				log.Fatal(err)
			}
			for _, source := range sources {
				filename := source.Name()
				if !strings.HasSuffix(filename, "_model.go") {
					continue
				}
				fset, b, err := parse(filepath.Join(d, filename))
				if err != nil {
					log.Fatal(err)
				}
				_, sourceTypes := impls(fset, b, filename)
				//ims = append(ims, sourceIms...)
				types = append(types, sourceTypes...)
			}
		}

	}

	typeMap := map[string]Type{}
	for _, t := range types {
		typeMap[t.Key()] = t
		//fmt.Printf("[TypeMap] %s : %v\n", t.Key(), t)
		//ast.Print(fset, v)
		// fmt.Printf("t: %s\n", t.FQName)
	}
	enums := map[string]map[string]bool{}
	for k := range typeMap {
		for s := range typeMap {
			if s != k && strings.HasPrefix(s, k) {
				if _, ok := enums[k]; !ok {
					enums[k] = map[string]bool{}
				}
				//fmt.Printf("::: %v <> %v\n", k, s)
				enums[k][s] = true
			}
		}
	}

	/*
		fmt.Printf("\x1b[4mTypes:\x1b[0m\n")
		for _, t := range types {
			fmt.Printf("  \x1b[33m%s\x1b[0m\n", t.Name)
			for _, f := range t.Fields {
				fmt.Printf("    \x1b[34m%s\x1b[0m %s\n", f.Name, f.Type)
			}
		}
		fmt.Println()
	*/

	imMap := map[string]Impl{}
	for _, im := range ims {
		imMap[im.Fun] = im
	}

	fmt.Printf("\x1b[4mRoutes:\x1b[0m\n")
	for _, r := range routes {
		fmt.Printf("\x1b[33m%7.7s\x1b[0m %s \x1b[36m[%s]\x1b[0m", r.Verb, hi(r.Path), r.Fun)
		if im, ok := imMap[r.Fun]; ok {
			fmt.Printf(" \x1b[34m%s\x1b[0m\n", im.Source)
			for _, c := range im.Comments {
				fmt.Printf("        \x1b[30;1m# %s\x1b[0m\n", c)
			}

			for _, p := range im.UriParams {
				fmt.Printf("        \x1b[35m· /\x1b[0m\x1b[1;35m%s\x1b[0m\n", uriParams[p])
			}
			for _, p := range im.QueryParams {
				fmt.Printf("        \x1b[34m· ?\x1b[0m\x1b[1;34m%s\x1b[0;34m=\x1b[0m\n", queryParams[p])
			}
			for _, p := range im.BodyParams {
				fmt.Printf("        \x1b[36m· {\x1b[0m")
				if t, ok := typeMap[p]; ok {
					pfx := "          "
					printType(t, typeMap, enums, 0, pfx)
				}
			}

			for statusCode, resp := range im.Resp {
				clr := "41;33;1"
				if statusCode < 300 {
					clr = "42;37;1"
				}
				pfx := "        " + strings.Repeat(" ", int(math.Log10(float64(statusCode)))+1) + "  "
				fmt.Printf("        \x1b[%sm%d\x1b[0m ", clr, statusCode)
				printType(resp.Type, typeMap, enums, 0, pfx)
			}
		} else {
			fmt.Printf(" ❌ not found\n")
		}
		fmt.Println()
	}
}
