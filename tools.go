package main

import (
	"fmt"
	"go/ast"
	"go/token"
	"slices"
	"strings"
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

var builtins = []string{
	"bool",
	"string",
	"int",
	"uint",
	"time.Time",
	"[]string",
	"[]bool",
	"[]int",
	"[]uint",
	"[]time.Time",
}

func isBuiltinType(t string) bool {
	return slices.Contains(builtins, t)
}

func isBuiltinSelectorType(pkg string, name string) bool {
	return slices.Contains(builtins, pkg+"."+name)
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

func nameOf(expr ast.Expr, pkg string) string {
	switch e := expr.(type) {
	case *ast.Ident:
		if pkg != "" {
			return pkg + "." + e.Name
		} else {
			return e.Name
		}
	case *ast.SelectorExpr:
		if x, ok := isIdent(e.X); ok {
			return x.Name + "." + e.Sel.Name
		} else {
			panic(fmt.Sprintf("typeName(): unsupported SelectorExpr type: %T %v", expr, expr))
		}
	case *ast.ArrayType:
		return fmt.Sprintf("[]%s", nameOf(e.Elt, pkg))
	case *ast.MapType:
		return fmt.Sprintf("map[%s]%s", nameOf(e.Key, pkg), nameOf(e.Value, pkg))
	}
	panic(fmt.Sprintf("nameOf(): unsupported expression type: %T %v", expr, expr))
}

func keysOf[K comparable, V any](m map[K]V) []K {
	r := make([]K, len(m))
	i := 0
	for k := range m {
		r[i] = k
		i++
	}
	return r
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

func methodNameOf(w *ast.CallExpr, recv string) string {
	if w.Fun != nil {
		switch f := w.Fun.(type) {
		case *ast.SelectorExpr:
			if ident(f.X, recv) {
				return nameOf(f.Sel, "")
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
