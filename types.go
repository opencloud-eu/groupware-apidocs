package main

import (
	"fmt"
	"go/types"
	"regexp"
	"slices"
)

type Field struct {
	Name string
	Attr string
	Type Type
	Tag  string
}

var tagRegex = regexp.MustCompile(`json:"(.+?)(?:,(omitempty|omitzero))?"`)

func newField(name string, t Type, tag string) (Field, bool) {
	attr := ""
	if m := tagRegex.FindAllStringSubmatch(tag, 2); m != nil {
		attr = m[0][1]
	}
	if attr == "-" {
		return Field{}, false
	}
	return Field{
		Name: name,
		Attr: attr,
		Type: t,
		Tag:  tag,
	}, true
}

type Type interface {
	Key() string
	Name() string
	IsBasic() bool
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

func (t AliasType) Name() string {
	return t.name
}

func (t AliasType) IsArray() bool {
	return false
}

func (t AliasType) IsMap() bool {
	return false
}

func (t AliasType) IsBasic() bool {
	return false
}

func (t AliasType) Deref() (Type, bool) {
	return t.typeRef, true
}

func (t AliasType) String() string {
	if t.pkg != "" {
		return t.pkg + "." + t.name
	} else {
		return t.name
	}
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

func (t InterfaceType) Name() string {
	return t.name
}

func (t InterfaceType) IsArray() bool {
	return false
}

func (t InterfaceType) IsMap() bool {
	return false
}

func (t InterfaceType) IsBasic() bool {
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

func (t BuiltinType) Name() string {
	return t.name
}

func (t BuiltinType) IsArray() bool {
	return false
}

func (t BuiltinType) IsMap() bool {
	return false
}

func (t BuiltinType) IsBasic() bool {
	return true
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

func (t ArrayType) Name() string {
	return t.elt.Name()
}

func (t ArrayType) IsArray() bool {
	return true
}

func (t ArrayType) IsMap() bool {
	return false
}

func (t ArrayType) IsBasic() bool {
	return t.elt.IsBasic()
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
	return t.elt, true
}

var _ Type = ArrayType{}

func newStructType(pkg string, name string, fields []Field) StructType {
	return StructType{pkg: pkg, name: name, fields: fields}
}

type StructType struct {
	pkg    string
	name   string
	fields []Field
}

func (t StructType) Key() string {
	return t.String()
}

func (t StructType) Name() string {
	return t.name
}

func (t StructType) IsArray() bool {
	return false
}

func (t StructType) IsMap() bool {
	return false
}

func (t StructType) IsBasic() bool {
	return false
}

func (t StructType) String() string {
	return t.pkg + "." + t.name
}

func (t StructType) Deref() (Type, bool) {
	return nil, false
}

func (t StructType) Fields() []Field {
	return t.fields
}

func (t StructType) Element() (Type, bool) {
	return nil, false
}

var _ Type = StructType{}

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

func (t MapType) Name() string {
	return t.value.Name()
}

func (t MapType) IsArray() bool {
	return false
}

func (t MapType) IsMap() bool {
	return true
}

func (t MapType) IsBasic() bool {
	return t.value.IsBasic()
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

func typeOf(t types.Type, mem map[string]Type) Type {
	switch t := t.(type) {
	case *types.Named:
		name := t.Obj().Name()
		pkg := ""
		if t.Obj().Pkg() != nil {
			pkg = t.Obj().Pkg().Name()
			if isBuiltinSelectorType(pkg, name) { // for things like time.Time
				return newBuiltinType(pkg, name)
			}
		} else {
			if isBuiltinType(name) {
				return newBuiltinType("", name)
			}
		}
		switch u := t.Underlying().(type) {
		case *types.Basic:
			return newAliasType(pkg, name, newBuiltinType("", u.Name()))
		case *types.Interface:
			return newInterfaceType(pkg, name)
		case *types.Map:
			return newMapType(typeOf(u.Key(), mem), typeOf(u.Elem(), mem))
		case *types.Array:
			return newArrayType(typeOf(u.Elem(), mem))
		case *types.Slice:
			return newArrayType(typeOf(u.Elem(), mem))
		case *types.Pointer:
			return typeOf(u.Elem(), mem)
		case *types.Struct:
			id := fmt.Sprintf("%s.%s", pkg, name)
			if ex, ok := mem[id]; ok {
				return ex
			}
			fields := []Field{}
			r := newStructType(pkg, name, fields)
			mem[id] = r
			for i := range u.NumFields() {
				f := u.Field(i)
				switch f.Type().Underlying().(type) {
				case *types.Signature:
					// skip methods
				default:
					typ := typeOf(f.Type(), mem)
					tag := u.Tag(i)
					if field, ok := newField(f.Name(), typ, tag); ok {
						fields = append(fields, field)
					}
				}
			}
			r.fields = fields
			return r
		default:
			panic(fmt.Sprintf("! underlying of named %s.%s is not struct but %T", pkg, name, u))
		}
	case *types.Basic:
		return newBuiltinType("", t.Name())
	case *types.Map:
		return newMapType(typeOf(t.Key(), mem), typeOf(t.Elem(), mem))
	case *types.Array:
		return newArrayType(typeOf(t.Elem(), mem))
	case *types.Slice:
		return newArrayType(typeOf(t.Elem(), mem))
	case *types.Pointer:
		return typeOf(t.Elem(), mem)
	case *types.Alias:
		pkg := ""
		if t.Obj().Pkg() != nil {
			pkg = t.Obj().Pkg().Name()
		}
		return newAliasType(pkg, t.Obj().Name(), typeOf(t.Underlying(), mem))
	case *types.Interface:
		if t.String() == "any" {
			return newBuiltinType("", "any")
		} else {
			panic(fmt.Sprintf("interface? %v\n", t))
		}
	case *types.TypeParam:
		// ignore
		return nil
	case *types.Chan:
		// ignore
		return nil
	case *types.Struct:
		// ignore unnamed struct
		return nil
	default:
		panic(fmt.Sprintf("unsupported: ?: %T: %#v", t, t))
	}
}

var builtins = []string{
	"any",
	"bool",
	"string",
	"int",
	"uint",
	"error",
}

func isBuiltinType(t string) bool {
	return slices.Contains(builtins, t)
}

func isBuiltinSelectorType(pkg string, _ string) bool {
	return !slices.Contains(packagesOfInterest, pkg)
}
