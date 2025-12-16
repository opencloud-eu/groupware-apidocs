package main

import (
	"fmt"
	"go/ast"
	"go/token"
	"log"
)

func typeRef(fset *token.FileSet, name string, expr ast.Expr, pkg string) Type {
	switch t := expr.(type) {
	case *ast.StructType:
		fields := []Field{}
		if t.Fields != nil && t.Fields.List != nil {
			for _, f := range t.Fields.List {
				if f != nil && f.Tag != nil {
					fieldName := typeName(f.Names)
					fields = append(fields, Field{
						Pkg:  pkg,
						Name: fieldName,
						Type: typeRef(fset, fieldName, f.Type, pkg),
						Tag:  tagOf(f),
					})
				}
			}
		}
		return newCustomType(pkg, name, fields)
	case *ast.Ident:
		if isBuiltinType(t.Name) {
			return newBuiltinType("", t.Name)
		} else {
			return newCustomType(pkg, t.Name, []Field{})
		}
	case *ast.StarExpr:
		return typeRef(fset, name, t.X, pkg)
	case *ast.SelectorExpr:
		if x, ok := isIdent(t.X); ok {
			if isBuiltinSelectorType(x.Name, t.Sel.Name) {
				return newBuiltinType(x.Name, t.Sel.Name)
			} else {
				return newCustomType(x.Name, t.Sel.Name, []Field{})
			}
		} else {
			panic(fmt.Sprintf("typeName(): unsupported SelectorExpr type: %T %v", expr, expr))
		}
	case *ast.MapType:
		return newMapType(typeRef(fset, "key", t.Key, pkg), typeRef(fset, "value", t.Value, pkg))
	case *ast.ArrayType:
		return newArrayType(typeRef(fset, name, t.Elt, pkg))
	default:
		ast.Print(fset, expr)
		log.Fatalf("typeRef: unsupported type %T", expr)
		return nil
	}
}

type Type interface {
	Key() string
	IsArray() bool
	IsMap() bool
	Deref() (Type, bool)
	String() string
	Fields() []Field
	Element() (Type, bool)
}

func newAliasType(pkg string, name string, typeRef Type) AliasType {
	return AliasType{pkg: pkg, name: name, typeRef: typeRef}
}

type AliasType struct {
	pkg     string
	name    string
	typeRef Type
}

func (t AliasType) Key() string {
	return t.String()
}

func (t AliasType) IsArray() bool {
	return false
}

func (t AliasType) IsMap() bool {
	return false
}

func (t AliasType) Deref() (Type, bool) {
	return t.typeRef, true
}

func (t AliasType) String() string {
	return t.pkg + "." + t.name
}

func (t AliasType) Fields() []Field {
	return []Field{}
}

func (t AliasType) Element() (Type, bool) {
	return nil, false
}

var _ Type = AliasType{}

func newInterfaceType(pkg string, name string) InterfaceType {
	return InterfaceType{pkg: pkg, name: name}
}

type InterfaceType struct {
	pkg  string
	name string
}

func (t InterfaceType) Key() string {
	return t.String()
}

func (t InterfaceType) IsArray() bool {
	return false
}

func (t InterfaceType) IsMap() bool {
	return false
}

func (t InterfaceType) Deref() (Type, bool) {
	return nil, false
}

func (t InterfaceType) String() string {
	return t.pkg + "." + t.name
}

func (t InterfaceType) Fields() []Field {
	return []Field{}
}

func (t InterfaceType) Element() (Type, bool) {
	return nil, false
}

var _ Type = InterfaceType{}

func newBuiltinType(pkg string, name string) BuiltinType {
	return BuiltinType{pkg: pkg, name: name}
}

type BuiltinType struct {
	pkg  string
	name string
}

func (t BuiltinType) Key() string {
	if t.pkg != "" {
		return t.pkg + "." + t.name
	} else {
		return t.name
	}
}

func (t BuiltinType) IsArray() bool {
	return false
}

func (t BuiltinType) IsMap() bool {
	return false
}

func (t BuiltinType) Deref() (Type, bool) {
	return nil, false
}

func (t BuiltinType) String() string {
	if t.pkg != "" {
		return t.pkg + "." + t.name
	} else {
		return t.name
	}
}

func (t BuiltinType) Fields() []Field {
	return []Field{}
}

func (t BuiltinType) Element() (Type, bool) {
	return nil, false
}

var _ Type = BuiltinType{}

func newArrayType(elt Type) ArrayType {
	return ArrayType{elt: elt}
}

type ArrayType struct {
	elt Type
}

func (t ArrayType) Key() string {
	return t.elt.Key()
}

func (t ArrayType) IsArray() bool {
	return true
}

func (t ArrayType) IsMap() bool {
	return false
}

func (t ArrayType) Deref() (Type, bool) {
	if d, ok := t.elt.Deref(); ok {
		return newArrayType(d), true
	} else {
		return nil, false
	}
}

func (t ArrayType) String() string {
	return "[]" + t.elt.String()
}

func (t ArrayType) Fields() []Field {
	return []Field{}
}

func (t ArrayType) Element() (Type, bool) {
	return t.elt, false
}

var _ Type = ArrayType{}

func newCustomType(pkg string, name string, fields []Field) CustomType {
	return CustomType{pkg: pkg, name: name, fields: fields}
}

type CustomType struct {
	pkg    string
	name   string
	fields []Field
}

func (t CustomType) Key() string {
	return t.String()
}

func (t CustomType) IsArray() bool {
	return false
}

func (t CustomType) IsMap() bool {
	return false
}

func (t CustomType) String() string {
	return t.pkg + "." + t.name
}

func (t CustomType) Deref() (Type, bool) {
	return nil, false
}

func (t CustomType) Fields() []Field {
	return t.fields
}

func (t CustomType) Element() (Type, bool) {
	return nil, false
}

var _ Type = CustomType{}

func newMapType(key Type, value Type) MapType {
	return MapType{key: key, value: value}
}

type MapType struct {
	key   Type
	value Type
}

func (t MapType) Key() string {
	return t.String()
}

func (t MapType) IsArray() bool {
	return false
}

func (t MapType) IsMap() bool {
	return false
}

func (t MapType) Deref() (Type, bool) {
	k, kok := t.key.Deref()
	v, vok := t.value.Deref()
	if kok || vok {
		if !kok {
			k = t.key
		}
		if !vok {
			v = t.value
		}
		return newMapType(k, v), true
	} else {
		return nil, false
	}
}

func (t MapType) String() string {
	return "map[" + t.key.String() + "]" + t.value.String()
}

func (t MapType) Fields() []Field {
	return []Field{}
}

func (t MapType) Element() (Type, bool) {
	return t.value, true
}

var _ Type = MapType{}
