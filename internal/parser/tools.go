package parser

import (
	"cmp"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"
	"opencloud.eu/groupware-apidocs/internal/model"
	"opencloud.eu/groupware-apidocs/internal/tools"
)

func isFQMethodCall(call *ast.CallExpr, pkg *packages.Package,
	pkgPath string,
	objNamePredicate func(string) bool,
	funcNamePredicate func(string) bool) bool {
	_, _, _, ok := expandFQMethodCall(call, pkg, pkgPath, objNamePredicate, funcNamePredicate)
	return ok
}

func expandFQMethodCall(call *ast.CallExpr, pkg *packages.Package,
	pkgPath string,
	objNamePredicate func(string) bool,
	funcNamePredicate func(string) bool) (string, string, string, bool) {
	if s, ok := isSelector(call.Fun); ok {
		if i, ok := isIdent(s.Sel); ok {
			if funcNamePredicate(i.Name) {
				if sel, ok := pkg.TypesInfo.Selections[s]; ok {
					t := sel.Recv()
					if p, ok := isPointer(t); ok {
						t = p.Elem()
					}
					if n, ok := isNamed(t); ok && objNamePredicate(n.Obj().Name()) {
						if pkgPath == "" && n.Obj().Pkg() == nil {
							return "", n.Obj().Name(), i.Name, true
						} else if n.Obj().Pkg() != nil && n.Obj().Pkg().Path() == pkgPath {
							return n.Obj().Pkg().Path(), n.Obj().Name(), i.Name, true
						}
					}
				}
			}
		}
	}
	return "", "", "", false
}

func isFQFuncDecl(decl ast.Decl, pkg *packages.Package, objname string, fname string) *ast.FuncDecl {
	switch f := decl.(type) {
	case *ast.FuncDecl:
		if f.Name.Name == fname {
			if d, ok := pkg.TypesInfo.Defs[f.Name]; ok {
				switch ff := d.(type) {
				case *types.Func:
					t := ff.Signature().Recv().Type()
					if p, ok := isPointer(t); ok {
						t = p.Elem()
					}
					if n, ok := isNamed(t); ok {
						if n.Obj().Name() == objname {
							return f
						}
					}
				}
			}
		}
	}
	return nil
}

func isMethodCall(call *ast.CallExpr, pkg string, method string) bool {
	if s, ok := isSelector(call.Fun); ok {
		if x, ok := isIdent(s.X); ok && s.Sel.Name == method && x.Obj != nil {
			if f, ok := isField(x.Obj.Decl); ok {
				if p, ok := isIdent(f.Type); ok && p.Name == pkg {
					return true
				}
			}
		}
	}
	return false
}

func isMethodCallPrefix(call *ast.CallExpr, pkg string, methodPrefixes []string) bool {
	if s, ok := isSelector(call.Fun); ok {
		if x, ok := isIdent(s.X); ok && tools.HasAnyPrefix(s.Sel.Name, methodPrefixes) && x.Obj != nil {
			if f, ok := isField(x.Obj.Decl); ok {
				if p, ok := isIdent(f.Type); ok && p.Name == pkg {
					return true
				}
			}
		}
	}
	return false
}

func isMethodCallRegex(call *ast.CallExpr, pkg string, re *regexp.Regexp, n int) [][]string {
	if s, ok := isSelector(call.Fun); ok {
		if x, ok := isIdent(s.X); ok && x.Obj != nil {
			m := re.FindAllStringSubmatch(s.Sel.Name, n)
			if m != nil {
				if f, ok := isField(x.Obj.Decl); ok {
					if t, ok := isIdent(f.Type); ok && t.Name == pkg {
						return m
					}
				}
			}
		}
	}
	return nil
}

func isStaticFunc(call *ast.CallExpr, pkg string, name string) bool {
	s, ok := isSelector(call.Fun)
	if !ok {
		return false
	}
	if m, ok := isIdent(s.Sel); !(ok && m.Name == name) {
		return false
	}
	if p, ok := isIdent(s.X); !(ok && p.Name == pkg) {
		return false
	}
	return true
}

func ident(expr ast.Expr, name string) bool {
	if expr != nil {
		switch v := expr.(type) {
		case *ast.Ident:
			return v.Name == name
		}
	}
	return false
}

func valueSpecNameFunc(v *ast.ValueSpec, predicate func(string) bool) (int, string) {
	if v != nil {
		for i, ident := range v.Names {
			if ident != nil && predicate(ident.Name) {
				return i, ident.Name
			}
		}
	}
	return -1, ""
}

func isComposite(expr ast.Expr) (*ast.CompositeLit, bool) {
	switch c := expr.(type) {
	case *ast.CompositeLit:
		return c, true
	}
	return nil, false
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

func nameOf(expr ast.Expr, pkg string) (string, error) {
	switch e := expr.(type) {
	case *ast.Ident:
		if model.IsBuiltinType(e.Name) {
			return e.Name, nil
		} else if pkg != "" {
			return pkg + "." + e.Name, nil
		} else {
			return e.Name, nil
		}
	case *ast.SelectorExpr:
		if x, ok := isIdent(e.X); ok {
			return x.Name + "." + e.Sel.Name, nil
		} else {
			return "", fmt.Errorf("typeName(): unsupported SelectorExpr type: %T %v", expr, expr)
		}
	case *ast.ArrayType:
		if deref, err := nameOf(e.Elt, pkg); err == nil {
			return fmt.Sprintf("[]%s", deref), nil
		} else {
			return "", nil
		}
	case *ast.MapType:
		keyDeref, err := nameOf(e.Key, pkg)
		if err != nil {
			return "", err
		}
		valueDeref, err := nameOf(e.Value, pkg)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("map[%s]%s", keyDeref, valueDeref), nil
	}
	return "", fmt.Errorf("nameOf(): unsupported expression type: %T %v", expr, expr)
}

func valuesOf[K comparable, V any](m map[K]V) []V {
	return slices.Collect(maps.Values(m))
}

func isCallExpr(expr ast.Expr) (*ast.CallExpr, bool) {
	switch e := expr.(type) {
	case *ast.CallExpr:
		return e, true
	}
	return nil, false
}

func stmtIsCallExpr(stmt ast.Stmt) *ast.CallExpr {
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

func methodNameOf(w *ast.CallExpr, recv string) (string, error) {
	if w.Fun != nil {
		switch f := w.Fun.(type) {
		case *ast.SelectorExpr:
			if ident(f.X, recv) {
				return nameOf(f.Sel, "")
			}
		}
	}
	return "", nil
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

type Const struct {
	Name     string
	Value    string
	Comments []string
}

func consts(decl ast.Decl) ([]Const, error) {
	results := []Const{}
	switch v := decl.(type) {
	case *ast.GenDecl:
		for _, s := range v.Specs {
			if a, ok := isValueSpec(s); ok {
				name := ""
				if len(a.Names) == 1 {
					name = a.Names[0].Name
				} else {
					return nil, fmt.Errorf("const: %T has more than 1 names: %v", a, a.Names)
				}
				value := ""
				if len(a.Values) == 1 {
					switch b := a.Values[0].(type) {
					case *ast.BasicLit:
						if b.Kind == token.STRING {
							value = strings.Trim(b.Value, "\"")
						} else {
							return nil, fmt.Errorf("const: %T '%s' has an unsupported kind: %v", a, name, b.Kind)
						}
					}
				} else {
					return nil, fmt.Errorf("const: %T '%s', has more than 1 values: %v", a, name, a.Values)
				}
				comments := []string{}
				if a.Comment != nil {
					for _, c := range a.Comment.List {
						comments = append(comments, c.Text)
					}
				}
				if name != "" {
					results = append(results, Const{Name: name, Value: value, Comments: comments})
				}
			}
		}
	}
	return results, nil
}

func isString(expr ast.Expr) (string, bool) {
	switch b := expr.(type) {
	case *ast.BasicLit:
		if b.Kind == token.STRING {
			return strings.Trim(b.Value, "\""), true
		}
	}
	return "", false
}

func isInt(expr ast.Expr) (int, bool, error) {
	switch b := expr.(type) {
	case *ast.BasicLit:
		if b.Kind == token.INT {
			if i, err := strconv.Atoi(b.Value); err != nil {
				return 0, true, err
			} else {
				return i, true, nil
			}
		}
	}
	return 0, false, nil
}

func isPointer(expr types.Type) (*types.Pointer, bool) {
	switch p := expr.(type) {
	case *types.Pointer:
		return p, true
	}
	return nil, false
}

func isNamed(expr types.Type) (*types.Named, bool) {
	switch n := expr.(type) {
	case *types.Named:
		return n, true
	}
	return nil, false
}

func isKeyValueExpr(expr ast.Expr) (*ast.KeyValueExpr, bool) {
	switch k := expr.(type) {
	case *ast.KeyValueExpr:
		return k, true
	}
	return nil, false
}

func isGenDecl(decl ast.Decl) (*ast.GenDecl, bool) {
	switch g := decl.(type) {
	case *ast.GenDecl:
		return g, true
	}
	return nil, false
}

func isIdent(expr ast.Expr) (*ast.Ident, bool) {
	switch e := expr.(type) {
	case *ast.Ident:
		return e, true
	case *ast.StarExpr:
		return isIdent(e.X)
	}
	return nil, false
}

func isField(decl any) (*ast.Field, bool) {
	switch e := decl.(type) {
	case *ast.Field:
		return e, true
	}
	return nil, false
}

func isSelector(expr ast.Expr) (*ast.SelectorExpr, bool) {
	switch s := expr.(type) {
	case *ast.SelectorExpr:
		return s, true
	}
	return nil, false
}

func keysort[K cmp.Ordered, V any](m map[K]V) []K {
	c := make([]K, len(m))
	copy(c, slices.Collect(maps.Keys(m)))
	slices.Sort(c)
	return c
}

func isClosure(expr ast.Expr) (*ast.FuncLit, bool) {
	switch e := expr.(type) {
	case *ast.FuncLit:
		return e, true
	}
	return nil, false
}

func isValueSpec(expr any) (*ast.ValueSpec, bool) {
	switch e := expr.(type) {
	case *ast.ValueSpec:
		return e, true
	}
	return nil, false
}

func findGroupwareErrorCodeDefinitions(s ast.Spec) (map[string]string, error) {
	m := map[string]string{}
	if v, ok := isValueSpec(s); ok && v != nil {
		for i, ident := range v.Names {
			if ident != nil && strings.HasPrefix(ident.Name, "ErrorCode") {
				value := v.Values[i]
				if text, ok := isString(value); ok {
					m[ident.Name] = text
				}
			}
		}
	}
	return m, nil
}

func parseHttpStatuses(p *packages.Package) map[string]int {
	m := map[string]int{}
	for _, syn := range p.Syntax {
		for _, decl := range syn.Decls {
			if g, ok := isGenDecl(decl); ok {
				for _, s := range g.Specs {
					if v, ok := isValueSpec(s); ok && v != nil {
						for i, ident := range v.Names {
							if ident != nil && strings.HasPrefix(ident.Name, "Status") {
								value := v.Values[i]
								if text, ok, err := isInt(value); err != nil {
									panic(err)
								} else if ok {
									m[ident.Name] = text
								} else {
									panic(fmt.Errorf("http constant '%s' is not an int but a %T", ident.Name, value))
								}
							}
						}
					}
				}
			}
		}
	}
	return m
}

func parseVersion(p *packages.Package) string {
	for _, syn := range p.Syntax {
		for _, decl := range syn.Decls {
			if g, ok := isGenDecl(decl); ok {
				for _, s := range g.Specs {
					if v, ok := isValueSpec(s); ok && v != nil {
						for i, ident := range v.Names {
							if ident != nil && ident.Name == "LatestTag" {
								value := v.Values[i]
								if text, ok := isString(value); ok {
									return text
								}
							}
						}
					}
				}
			}
		}
	}
	return ""
}

func findGroupwareErrorDefinitions(s ast.Spec, groupwareErrorType model.Type, errorCodeDefinitions map[string]string, httpStatusMap map[string]int) (map[string]model.PotentialError, error) {
	m := map[string]model.PotentialError{}
	if v, ok := isValueSpec(s); ok {
		i, name := valueSpecNameFunc(v, func(name string) bool { return strings.HasPrefix(name, "Error") })
		if i < 0 {
			return m, nil
		}
		value := v.Values[i]
		if c, ok := isComposite(value); ok {
			if ident, ok := isIdent(c.Type); !(ok && ident.Name == groupwareErrorType.Name()) {
				return nil, nil
			}
			gwe := model.GroupwareError{}
			for _, elt := range c.Elts {
				if kve, ok := isKeyValueExpr(elt); ok {
					field := ""
					if ident, ok := isIdent(kve.Key); ok {
						field = ident.Name
					}
					if field == "" {
						panic(fmt.Errorf("elt kve Key is not an ident but a %T", kve.Key))
						//continue
					}
					switch field {
					case "Status":
						if s, ok := isSelector(kve.Value); ok {
							if pkgIdent, ok := isIdent(s.X); ok && pkgIdent.Name == "http" {
								httpStatusName := s.Sel.Name
								if code, ok := httpStatusMap[httpStatusName]; ok {
									gwe.Status = code
								} else {
									panic(fmt.Errorf("failed to map http status name '%s' to a code in httpStatusMap", httpStatusName))
								}
							} else {
								panic(fmt.Errorf("Status value is a selector but X is not an ident but a %T", s.X))
							}
						} else {
							panic(fmt.Errorf("Status value is not a selector but a %T", kve.Value))
						}
					case "Code":
						// expecting an ident to an error code constant
						if ident, ok := isIdent(kve.Value); ok {
							if text, ok := errorCodeDefinitions[ident.Name]; ok {
								gwe.Code = text
							} else {
								panic(fmt.Errorf("failed to find entry in errorCodeDefinitions for '%s'", ident.Name))
							}
						} else {
							panic(fmt.Errorf("elt kve Value for '%s' in '%s' is not an ident but a %T", field, name, kve.Value))
						}
					case "Title":
						// expecting a basiclit string value
						if text, ok := isString(kve.Value); ok {
							gwe.Title = text
						} else {
							panic(fmt.Errorf("elt kve Value for '%s' in '%s' is not a basiclit string but a %T", field, name, kve.Value))
						}
					case "Detail":
						// expecting a basiclit string value
						if text, ok := isString(kve.Value); ok {
							gwe.Detail = text
						} else {
							panic(fmt.Errorf("elt kve Value for '%s' in '%s' is not a basiclit string but a %T", field, name, kve.Value))
						}
					default:
						panic(fmt.Errorf("unsupported GroupwareError field '%s' in '%s'", field, name))
					}
				} else {
					panic(fmt.Errorf("elt is not a KeyValueExpr but a %T in '%s'", elt, name))
				}
			}
			// TODO we could add a check here to make sure every field has been set
			m[name] = model.PotentialError{
				Name:    name,
				Type:    groupwareErrorType,
				Payload: gwe,
			}
		}
	}
	return m, nil
}

func seqp(s string) func(string) bool {
	return func(str string) bool { return str == s }
}

func contp[T comparable](s []T) func(T) bool {
	return func(e T) bool { return slices.Contains(s, e) }
}

func truep[T any]() func(T) bool {
	return func(T) bool { return true }
}

func falsep[T any]() func(T) bool {
	return func(T) bool { return false }
}
