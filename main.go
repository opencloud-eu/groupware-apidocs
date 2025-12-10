package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/davecgh/go-spew/spew"
)

func ident(expr ast.Expr, name string) bool {
	if expr != nil {
		switch v := expr.(type) {
		case *ast.Ident:
			return v.Name == name
		}
	}
	return false
}

func translateType(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return fmt.Sprintf("*%s", translateType(t.X))
	case *ast.SelectorExpr:
		return fmt.Sprintf("%s.%s", nameOf(t.X), translateType(t.Sel))
	case *ast.MapType:
		return fmt.Sprintf("map[%s]%s", translateType(t.Key), translateType(t.Value))
	case *ast.ArrayType:
		return fmt.Sprintf("[]%s", translateType(t.Elt))
	default:
		spew.Dump(expr)
		log.Fatalf("unsupported type %T", expr)
		return ""
	}
}

func typeOf(field *ast.Field) string {
	if field == nil {
		return ""
	}
	return translateType(field.Type)
}

func tagOf(field *ast.Field) string {
	if field == nil || field.Tag == nil {
		return ""
	}
	if field.Tag.Kind == token.STRING {
		return field.Tag.Value
	} else {
		return ""
	}
}

func isParamType(field *ast.Field, pkg string, name string) bool {
	if field == nil || field.Type == nil {
		return false
	}
	switch w := field.Type.(type) {
	case *ast.SelectorExpr:
		if w.Sel != nil && w.Sel.Name == name && ident(w.X, pkg) {
			return true
		}
	}
	return false
}

func isParamPtrType(field *ast.Field, pkg string, name string) bool {
	if field == nil || field.Type == nil {
		return false
	}
	switch w := field.Type.(type) {
	case *ast.StarExpr:
		if w.X != nil {
			switch z := w.X.(type) {
			case *ast.SelectorExpr:
				if z.Sel != nil && z.Sel.Name == name && ident(z.X, pkg) {
					return true
				}
			}
		}
	}
	return false
}

func hasName(f *ast.FuncDecl, name string) bool {
	return f != nil && ident(f.Name, name)
}

func hasNumParams(f *ast.FuncDecl, n int) bool {
	return f != nil && f.Type != nil && f.Type.Params != nil && f.Type.Params.NumFields() == n
}

func isMemberOf(f ast.Decl, name string) bool {
	if f == nil {
		return false
	}
	switch v := f.(type) {
	case *ast.FuncDecl:
		if v.Recv != nil && v.Recv.NumFields() == 1 && v.Recv.List[0].Type != nil {
			switch w := v.Recv.List[0].Type.(type) {
			case *ast.StarExpr:
				if ident(w.X, name) {
					return true
				}
			}
		}
	}
	return false
}

func nameOf(f ast.Expr) string {
	switch v := f.(type) {
	case *ast.Ident:
		return v.Name
	default:
		log.Fatalf("unsupported type in nameOf(): %T: %v", f, f)
	}
	return ""
}

func typeName(names []*ast.Ident) string {
	parts := []string{}
	for _, n := range names {
		if n.Name != "" {
			parts = append(parts, n.Name)
		}
	}
	return strings.Join(parts, "")
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
	Type string
}

type Impl struct {
	Source   string
	Fun      string
	Comments []string
	Resp     map[int]Resp
}

type Field struct {
	Name string
	Type string
	Tag  string
}

type Type struct {
	Name   string
	Fields []Field
}

var verbs = []string{
	"Get",
	"Put",
	"Post",
	"Delete",
	"Patch",
}

func isCallExpr(stmt ast.Stmt) *ast.CallExpr {
	switch v := stmt.(type) {
	case *ast.ExprStmt:
		if v.X != nil {
			switch w := v.X.(type) {
			case *ast.CallExpr:
				return w
			}
		}
	}
	return nil
}

func methodNameOf(w *ast.CallExpr, recv string) string {
	if w.Fun != nil {
		switch f := w.Fun.(type) {
		case *ast.SelectorExpr:
			if ident(f.X, recv) {
				return nameOf(f.Sel)
			}
		}
	}
	return ""
}

func stringArgOf(call *ast.CallExpr, n int) string {
	if call != nil && len(call.Args) >= n+1 {
		switch z := call.Args[0].(type) {
		case *ast.BasicLit:
			if z.Kind == token.STRING {
				return strings.Trim(z.Value, "\"")
			}
		}
	}
	return ""
}

func funcArgOf(call *ast.CallExpr, n int) *ast.FuncLit {
	if call != nil && n < len(call.Args) {
		switch z := call.Args[n].(type) {
		case *ast.FuncLit:
			return z
		}
	}
	return nil
}

func funcRefArgOf(call *ast.CallExpr, n int) *ast.SelectorExpr {
	if call != nil && n < len(call.Args) {
		switch z := call.Args[n].(type) {
		case *ast.SelectorExpr:
			return z
		}
	}
	return nil
}

func join(parts ...string) string {
	result := make([]string, len(parts))
	for i, part := range parts {
		if i == 0 {
			if strings.HasPrefix(part, "/") {
				result[i] = part
			} else {
				result[i] = "/" + part
			}
		} else {
			if strings.HasSuffix(result[i-1], "/") {
				if strings.HasPrefix(part, "/") {
					result[i] = part[1:]
				} else {
					result[i] = part
				}
			} else {
				if strings.HasPrefix(part, "/") {
					result[i] = part
				} else {
					result[i] = "/" + part
				}
			}
		}
	}
	str := strings.Join(result, "")
	str = strings.TrimSuffix(str, "/")
	if str == "" {
		str = "/"
	}
	return str
}

func endpoints(base string, stmts []ast.Stmt) []Endpoint {
	result := []Endpoint{}
	for _, stmt := range stmts {
		if call := isCallExpr(stmt); call != nil {
			if verb := methodNameOf(call, "r"); verb != "" {
				if verb == "Route" {
					// recurse
					//spew.Dump(call)
					if path := stringArgOf(call, 0); path != "" {
						if deep := funcArgOf(call, 1); deep != nil {
							result = append(result, endpoints(join(base, path), deep.Body.List)...)
						}
					}
				} else if slices.Contains(verbs, verb) && len(call.Args) == 2 {
					if path := stringArgOf(call, 0); path != "" {
						if f := funcRefArgOf(call, 1); f != nil {
							//spew.Dump(f)
							fun := nameOf(f.Sel)
							result = append(result, Endpoint{Verb: strings.ToUpper(verb), Path: join(base, path), Fun: fun})
						}
					}
				}
			}
		}
	}
	return result
}

func isConstDecl(decl ast.Decl) map[string]string {
	result := map[string]string{}
	switch v := decl.(type) {
	case *ast.GenDecl:
		for _, s := range v.Specs {
			switch a := s.(type) {
			case *ast.ValueSpec:
				name := ""
				if len(a.Names) == 1 {
					name = a.Names[0].Name
				}
				value := ""
				if len(a.Values) == 1 {
					switch b := a.Values[0].(type) {
					case *ast.BasicLit:
						if b.Kind == token.STRING {
							value = strings.Trim(b.Value, "\"")
						}
					}
				}
				if name != "" {
					result[name] = value
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
var apiResponseRegex = regexp.MustCompile(`^//\s*api:response:(\d+):\s*(\S+)$`)

func impls(a *ast.File, source string) ([]Impl, []Type) {
	//fmt.Printf("[📃] \x1b[34;4;1m%s\x1b[0m\n", source)
	ims := []Impl{}
	types := []Type{}

	for _, decl := range a.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if isMemberOf(d, "Groupware") && hasNumParams(d, 2) && isParamType(d.Type.Params.List[0], "http", "ResponseWriter") && isParamPtrType(d.Type.Params.List[1], "http", "Request") {
				fun := nameOf(d.Name)
				comments := []string{}
				resp := map[int]Resp{}
				if d.Doc != nil && d.Doc.List != nil {
					for _, doc := range d.Doc.List {
						comment := strings.TrimSpace(doc.Text)
						if match := apiResponseRegex.FindAllStringSubmatch(comment, -1); len(match) > 0 {
							statusCode, _ := strconv.Atoi(match[0][1]) // TODO err
							ret := match[0][2]
							resp[statusCode] = Resp{Type: ret}
						}
						comments = append(comments, doc.Text)
					}
				}
				ims = append(ims, Impl{Source: source, Fun: fun, Comments: comments, Resp: resp})

				if d.Body != nil {
					for _, stmt := range d.Body.List {
						spew.Dump(stmt)
						/*
							switch s := stmt.(type) {
							}
						*/
					}
				}
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch v := spec.(type) {
				case *ast.TypeSpec:
					switch t := v.Type.(type) {
					case *ast.StructType:
						tname := v.Name.Name
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
									fields = append(fields, Field{Name: typeName(f.Names), Type: typeOf(f), Tag: tagOf(f)})
								}
							}
						}
						types = append(types, Type{Name: tname, Fields: fields})
					}
				}
			}
		}
	}

	// mailboxId := chi.URLParam(r, UriParamMailboxId)

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

func parse(filename string) (*ast.File, error) {
	fset := token.NewFileSet()
	src, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	a, err := parser.ParseFile(fset, filename, src, parser.ParseComments+parser.AllErrors)
	if err != nil {
		return nil, err
	}
	return a, nil
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
		a, err := parse("pkg/groupware/groupware_route.go")
		if err != nil {
			log.Fatal(err)
		}

		route := pickRoute(a)
		if route == nil {
			log.Fatal("failed to find Route() method")
		}

		routes = endpoints("/", route.Body.List)
		uriParams, queryParams = params(a.Decls)
	}
	{
		fmt.Printf("\x1b[4mURI Params:\x1b[0m\n")
		for k, v := range uriParams {
			fmt.Printf("  \x1b[33m%s\x1b[0m: %s\n", k, v)
		}
		fmt.Println()
		fmt.Printf("\x1b[4mQuery Params:\x1b[0m\n")
		for k, v := range queryParams {
			fmt.Printf("  \x1b[33m%s\x1b[0m: %s\n", k, v)
		}
		fmt.Println()
	}

	// load everything else and match the functions
	sources, err := os.ReadDir("pkg/groupware")
	if err != nil {
		log.Fatal(err)
	}
	ims := []Impl{}
	types := []Type{}
	for _, source := range sources {
		filename := source.Name()
		if !(strings.HasPrefix(filename, "groupware_") && strings.HasSuffix(filename, "go")) {
			continue
		}
		b, err := parse(filepath.Join("pkg/groupware", filename))
		if err != nil {
			log.Fatal(err)
		}
		sourceIms, sourceTypes := impls(b, filename)
		ims = append(ims, sourceIms...)
		types = append(types, sourceTypes...)
	}

	fmt.Printf("\x1b[4mTypes:\x1b[0m\n")
	for _, t := range types {
		fmt.Printf("  \x1b[33m%s\x1b[0m\n", t.Name)
		for _, f := range t.Fields {
			fmt.Printf("    \x1b[34m%s\x1b[0m %s\n", f.Name, f.Type)
		}
	}
	fmt.Println()

	imMap := map[string]Impl{}
	for _, im := range ims {
		imMap[im.Fun] = im
	}

	fmt.Printf("\x1b[4mRoutes:\x1b[0m\n")
	for _, r := range routes {
		fmt.Printf("\x1b[33m%7.7s\x1b[0m %s \x1b[36m[%s]\x1b[0m", r.Verb, hi(r.Path), r.Fun)
		if im, ok := imMap[r.Fun]; ok {
			fmt.Printf(" \x1b[34m%s\x1b[0m\n", im.Source)
			for statusCode, resp := range im.Resp {
				clr := "31"
				if statusCode < 300 {
					clr = "32"
				}
				fmt.Printf("        \x1b[%s;1;4m%d\x1b[0m: %s\n", clr, statusCode, resp.Type)
			}
		} else {
			fmt.Printf(" ❌ not found\n")
		}
	}
	fmt.Println()
}
