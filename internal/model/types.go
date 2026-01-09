package model

import (
	"slices"

	"opencloud.eu/groupware-apidocs/internal/config"
)

var builtins = []string{
	"any",
	"bool",
	"string",
	"int",
	"uint",
	"error",
}

func IsBuiltinType(t string) bool {
	return slices.Contains(builtins, t)
}

func IsBuiltinSelectorType(pkg string, _ string) bool {
	return !slices.Contains(config.PackagesOfInterest, pkg)
}

var (
	IntType    = NewBuiltinType("", "int")
	UIntType   = NewBuiltinType("", "uint")
	StringType = NewBuiltinType("", "string")
	BoolType   = NewBuiltinType("", "bool")
	TimeType   = NewBuiltinType("time", "Time")
)

func NewAliasType(pkg string, name string, typeRef Type) AliasType {
	if typeRef == nil {
		panic("elt is nil")
	}
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

func (t AliasType) Summary() string {
	return ""
}

func (t AliasType) Description() string {
	return ""
}

var _ Type = AliasType{}

func NewInterfaceType(pkg string, name string) InterfaceType {
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

func (t InterfaceType) Summary() string {
	return ""
}

func (t InterfaceType) Description() string {
	return ""
}

var _ Type = InterfaceType{}

func NewBuiltinType(pkg string, name string) BuiltinType {
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

func (t BuiltinType) Summary() string {
	return ""
}

func (t BuiltinType) Description() string {
	return ""
}

var _ Type = BuiltinType{}

func NewArrayType(elt Type) ArrayType {
	if elt == nil {
		panic("elt is nil")
	}
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
		return NewArrayType(d), true
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

func (t ArrayType) Summary() string {
	return t.elt.Summary()
}

func (t ArrayType) Description() string {
	return t.elt.Description()
}

var _ Type = ArrayType{}

func NewStructType(pkg string, name string, fields []Field, summary string, description string) StructType {
	return StructType{pkg: pkg, name: name, fields: fields, summary: summary, description: description}
}

type StructType struct {
	pkg         string
	name        string
	fields      []Field
	summary     string
	description string
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

func (t StructType) Summary() string {
	return t.summary
}

func (t StructType) Description() string {
	return t.description
}

var _ Type = StructType{}

func NewMapType(key Type, value Type) MapType {
	if key == nil {
		panic("key is nil")
	}
	if value == nil {
		panic("value is nil")
	}
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
		return NewMapType(k, v), true
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

func (t MapType) Summary() string {
	return t.value.Summary() // TODO how to document a map?
}

func (t MapType) Description() string {
	return t.value.Description() // TODO how to document a map?
}

var _ Type = MapType{}
