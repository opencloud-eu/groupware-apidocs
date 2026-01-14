package parser

import (
	"cmp"
	"fmt"
	"go/ast"
	"go/token"
	"maps"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"opencloud.eu/groupware-apidocs/internal/model"
	"opencloud.eu/groupware-apidocs/internal/tools"
)

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

func article(str string) string {
	if voweled(str) {
		return "an"
	} else {
		return "a"
	}
}

func voweled(str string) bool {
	if len(str) < 1 {
		return false
	}
	c, _ := utf8.DecodeRuneInString(str)
	switch c {
	case 'a', 'i', 'e', 'o', 'u', 'y', 'A', 'I', 'E', 'O', 'U', 'Y':
		return true
	}
	return false
}

func singularize(str string) string {
	if strings.HasSuffix(str, "ies") {
		return str[0:len(str)-3] + "y"
	}
	if strings.HasSuffix(str, "es") {
		return str[0 : len(str)-2]
	}
	if strings.HasSuffix(str, "s") {
		return str[0 : len(str)-1]
	}
	return str
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
