package parser

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"log"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"
	"opencloud.eu/groupware-apidocs/internal/config"
	"opencloud.eu/groupware-apidocs/internal/model"
	"opencloud.eu/groupware-apidocs/internal/tools"
)

type responseFunc struct {
	bodyArgPos int
	statusCode int
}

type returnVisitor struct {
	fun                            string
	fn                             *types.Func
	fset                           *token.FileSet
	typeMap                        map[string]model.Type
	groupwarePkg                   *packages.Package
	responses                      map[int]model.Resp
	successfulResponseFuncs        map[string]responseFunc
	successfulRequestResponseFuncs map[string]responseFunc
	potentialErrors                map[string]bool
	exampleKey                     string
	defaultValue                   string
	undocumented                   map[string]token.Position
	errs                           *[]error
}

func newReturnVisitor(fun string, fn *types.Func, fset *token.FileSet, groupwarePkg *packages.Package, typeMap map[string]model.Type,
	successfulResponseFuncs map[string]responseFunc,
	successfulRequestResponseFuncs map[string]responseFunc,
	exampleKey string,
	defaultValue string) returnVisitor {
	return returnVisitor{
		fun:                            fun,
		fn:                             fn,
		fset:                           fset,
		groupwarePkg:                   groupwarePkg,
		typeMap:                        typeMap,
		responses:                      map[int]model.Resp{},
		potentialErrors:                map[string]bool{},
		successfulResponseFuncs:        successfulResponseFuncs,
		successfulRequestResponseFuncs: successfulRequestResponseFuncs,
		exampleKey:                     exampleKey,
		defaultValue:                   defaultValue,
		undocumented:                   map[string]token.Position{},
		errs:                           &[]error{},
	}
}

var (
	MissingAccountError  = "ErrorNonExistingAccount"
	MissingObjTypeErrors = map[string][]string{
		"Contact":  {"ErrorMissingContactsSessionCapability", "ErrorMissingContactsAccountCapability"},
		"Calendar": {"ErrorMissingCalendarsSessionCapability", "ErrorMissingCalendarsAccountCapability"},
		"Task":     {"ErrorMissingTasksSessionCapability", "ErrorMissingTasksAccountCapability"},
	}
	GlobalPotentialErrors = []string{
		"ErrorForbidden",
		"ErrorInvalidBackendRequest",
		"ErrorServerResponse",
		"ErrorReadingResponse",
		"ErrorProcessingResponse",
		"ErrorEncodingRequestBody",
		"ErrorCreatingRequest",
		"ErrorSendingRequest",
		"ErrorInvalidSessionResponse",
		"ErrorInvalidRequestPayload",
		"ErrorInvalidResponsePayload",
		"ErrorServerUnavailable",
		"ErrorServerFailure",
		"ErrorForbiddenOperation",
	}
	GlobalPotentialErrorsForQueryParams = []string{
		"ErrorInvalidRequestParameter",
	}
	GlobalPotentialErrorsForMandatoryQueryParams = []string{
		"ErrorMissingMandatoryRequestParameter",
	}
	GlobalPotentialErrorsForPathParams = []string{
		"ErrorInvalidRequestParameter",
	}
	GlobalPotentialErrorsForMandatoryPathParams = []string{
		"ErrorMissingMandatoryRequestParameter",
	}
	GlobalPotentialErrorsForBodyParams = []string{
		"ErrorInvalidRequestBody",
	}
	GlobalPotentialErrorsForMandatoryBodyParams = []string{
		"ErrorMissingMandatoryRequestParameter",
	}
	GlobalPotentialErrorsForAccount = []string{
		"ErrorNonExistingAccount",
	}

	// "ErrorAccountNotFound",
	// "ErrorAccountNotSupportedByMethod",
	// "ErrorAccountReadOnly",
)

func (v returnVisitor) isErrorResponseFunc(r *ast.ReturnStmt) bool {
	for _, result := range r.Results {
		if call, ok := isCallExpr(result); ok {
			if len(call.Args) == 2 && isStaticFunc(call, "Groupware", "errorResponse") {
				//a := result.Args[1]
				//var _ = a
				panic("todo: extract error from errorResponse")
				//return true
			}
		} else if ident, ok := isIdent(result); ok {
			//obj := v.pkg.TypesInfo.Uses[ident]
			//tv := v.pkg.TypesInfo.Defs[ident]
			if t, ok := v.groupwarePkg.TypesInfo.Types[ident]; ok && t.Type != nil {
				if n, ok := isNamed(t.Type); ok {
					if n.Obj() != nil && n.Obj().Name() == "Response" && n.Obj().Pkg() != nil && n.Obj().Pkg().Name() == "groupware" {
						switch decl := ident.Obj.Decl.(type) {
						case *ast.AssignStmt:
							for _, rhs := range decl.Rhs {
								if call, ok := isCallExpr(rhs); ok {
									if p, neededType, ok := isNeedAccountCall(call, v.groupwarePkg, v.exampleKey); ok && p.Required {
										v.potentialErrors[MissingAccountError] = true
										if moreErrs, ok := MissingObjTypeErrors[neededType]; ok {
											for _, e := range moreErrs {
												v.potentialErrors[e] = true
											}
										} else {
											log.Panicf("failed to find MissingObjTypeErrors entry for neededType='%s'", neededType)
										}
									}
								}
							}
						}
						// TODO try to detect more error responses
					}
				}
			}
		}
	}
	return false
}

func (v returnVisitor) isSuccessfulResponseFunc(r *ast.ReturnStmt) (ast.Expr, string, int, bool) {
	if r == nil {
		return nil, "", 0, false
	}
	if c, ok := isCallExpr(r.Results[0]); ok {
		if i, ok := isIdent(c.Fun); ok {
			for f, spec := range v.successfulResponseFuncs {
				if i.Name == f {
					summary := ""
					if cg := findInlineComment(c.Pos(), v.groupwarePkg); cg != nil && len(cg.List) > 0 {
						summary = strings.Join(processComments(lines(cg.List)), "\n") // TODO parse one-liner response function comment for api tags if needed
					}
					var body ast.Expr = nil
					if spec.bodyArgPos >= 0 {
						body = c.Args[spec.bodyArgPos]
					}
					return body, summary, spec.statusCode, true
				}
			}
		}
		if _, _, i, ok := expandFQMethodCall(c, v.groupwarePkg, config.GroupwarePackageID, seqp("Request"), tools.Truth1); ok {
			for f, spec := range v.successfulRequestResponseFuncs {
				if f == i {
					summary := ""
					if cg := findInlineComment(c.Pos(), v.groupwarePkg); cg != nil && len(cg.List) > 0 {
						summary = strings.Join(processComments(lines(cg.List)), "\n") // TODO parse one-liner response function comment for api tags if needed
					}
					var body ast.Expr = nil
					if spec.bodyArgPos >= 0 {
						body = c.Args[spec.bodyArgPos]
					}
					return body, summary, spec.statusCode, true
				}
			}
		}
	}
	return nil, "", 0, false
}

func (v returnVisitor) resolveIdent(x *ast.Ident) (model.Type, error) {
	z, ok := v.groupwarePkg.TypesInfo.Uses[x]
	if !ok {
		z, ok = v.groupwarePkg.TypesInfo.Defs[x]
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
	switch n := t.(type) {
	case *types.Named:
		name = nameType(n.Obj())
		pos = n.Obj().Pos()
	}
	r, err := typeOf(t, nil, v.typeMap, v.groupwarePkg)
	if err != nil {
		return nil, err
	}
	if r == nil {
		log.Panicf("failed to resolve type %v", t)
	} else {
		if r.Summary() == "" {
			if name != "" {
				v.undocumented[name] = v.groupwarePkg.Fset.Position(pos)
			}
		}
	}
	return r, nil
}

func (v returnVisitor) deduceArgType(code int, arg ast.Expr, pkg *packages.Package) (int, model.Type, error) {
	switch arg := arg.(type) {
	case *ast.SelectorExpr:
		if x, ok := pkg.TypesInfo.Selections[arg]; ok {
			if n, ok := isNamed(x.Recv()); ok {
				key := n.Obj().Name()
				if n.Obj().Pkg() != nil && n.Obj().Pkg().Name() != "" {
					key = n.Obj().Pkg().Name() + "." + key
				}
				if key == "jmap.Result" {
					// unwrap
					targs := n.TypeArgs()
					if targs != nil && targs.Len() == 1 {
						if t, err := typeOf(targs.At(0), nil, v.typeMap, v.groupwarePkg); err != nil {
							return 0, nil, err
						} else {
							return code, t, nil
						}
					} else {
						return 0, nil, fmt.Errorf("%s has no typeargs", key)
					}
				} else {
					if m, ok := v.typeMap[key]; ok {
						return code, m, nil
					} else {
						return 0, nil, fmt.Errorf("failed to find the type '%s' in the typeMap", key)
					}
				}
			} else {
				return 0, nil, fmt.Errorf("SelectorExpr is not Named")
			}
		} else {
			return 0, nil, fmt.Errorf("failed to find SelectorExpr in package")
		}
	case *ast.CompositeLit:
		switch t := arg.Type.(type) {
		case *ast.Ident:
			if t.Obj.Kind != ast.Typ {
				return 0, nil, fmt.Errorf("unsupported compositelit type is an ident but not of kind Typ: %T: %#v", t, t)
			} else {
				key := t.Obj.Name
				if !strings.Contains(key, ".") {
					if !model.IsBuiltinType(key) {
						key = name(v.groupwarePkg.Name, key)
					}
				}
				if m, ok := v.typeMap[key]; ok {
					return code, m, nil
				} else {
					buf := new(bytes.Buffer)
					if err := ast.Fprint(buf, v.fset, arg, nil); err == nil {
						return 0, nil, fmt.Errorf("failed to find the type '%s' in the typeMap, used as the response type for %#v in %s", key, arg, buf.String())
					} else {
						return 0, nil, fmt.Errorf("failed to find the type '%s' in the typeMap, used as the response type for %#v", key, arg)
					}
				}
			}
		default:
			return 0, nil, fmt.Errorf("unsupported compositelit type: %T: %#v", arg.Type, arg.Type)
		}
	case *ast.Ident:
		if arg.Obj == nil {
			return 0, nil, fmt.Errorf("response body argument is an ident that has a nil Obj: %v", arg)
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
					return 0, nil, fmt.Errorf("failed to find matching variable on LHS of assign statement")
				} else {
					if t, err := v.resolveIdent(a); err != nil {
						return 0, nil, err
					} else {
						return code, t, nil
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
									return 0, nil, fmt.Errorf("failed to parse integer status code in variable name '%v': %w", t.Name, err)
								}
							}
						}

						if t, err := v.resolveIdent(arg); err != nil {
							return 0, nil, err
						} else {
							return code, t, nil
						}
					} else {
						return 0, nil, fmt.Errorf("body result return variable is not an ident: %#v", d)
					}
				} else {
					return 0, nil, fmt.Errorf("d.Names len is not == 1 but %d", len(d.Names))
				}
			default:
				buf := new(bytes.Buffer)
				if err := ast.Fprint(buf, v.fset, arg, nil); err == nil {
					return 0, nil, fmt.Errorf("unsupported return decl: %T %v: %s", d, d, buf.String())
				} else {
					return 0, nil, fmt.Errorf("unsupported return decl: %T %v", d, d)
				}
			}
		}
	case *ast.CallExpr:
		if i, ok := isIdent(arg.Fun); ok {
			switch d := i.Obj.Decl.(type) {
			case *ast.FuncDecl:
				if d.Type.Results != nil && len(d.Type.Results.List) > 0 {
					ret := d.Type.Results.List[0].Type
					if tv, ok := v.groupwarePkg.TypesInfo.Types[ret]; ok {
						if t, err := v.resolveType(tv.Type); err != nil {
							return 0, nil, err
						} else if t == nil {
							return 0, nil, fmt.Errorf("failed to resolve tv")
						} else {
							return code, t, nil
						}
					} else {
						return 0, nil, fmt.Errorf("failed to find return type expr in TypesInfo")
					}
				} else {
					return 0, nil, fmt.Errorf("callexpr fun has no results")
				}
			default:
				return 0, nil, fmt.Errorf("callexpr fun is not a funcdecl")
			}

		} else {
			return 0, nil, fmt.Errorf("callexpr fun is not an ident")
		}
	case *ast.IndexExpr:
		switch x := arg.X.(type) {
		case *ast.SelectorExpr:
			if sel, ok := v.groupwarePkg.TypesInfo.Selections[x]; ok {
				p := sel.Obj().Type()
				switch u := p.(type) {
				case *types.Slice:
					switch n := u.Elem().(type) {
					case *types.Named:
						if t, err := v.resolveType(n); err != nil {
							return 0, nil, err
						} else {
							return code, t, nil
						}
					default:
						return 0, nil, fmt.Errorf("slice elem is not named")
					}
				default:
					return 0, nil, fmt.Errorf("sel is not a slice")
				}
			} else {
				def, ok := v.groupwarePkg.TypesInfo.Defs[x.Sel]
				if ok {
					buf := new(bytes.Buffer)
					ast.Fprint(buf, v.fset, arg, nil)
					log.Printf("definition of %s:\n%s", x.X.(*ast.Ident).Name, def.Name())
				} else {
					tv, ok := v.groupwarePkg.TypesInfo.Types[x.Sel]
					if !ok {
						return 0, nil, fmt.Errorf("failed to find something")
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
	return 0, nil, fmt.Errorf("failed to resolve return type")
}

func (v returnVisitor) Visit(n ast.Node) ast.Visitor {
	if n == nil { // = nothing left to visit
		return nil
	}
	r, ok := n.(*ast.ReturnStmt)
	if !ok {
		return v
	}

	if arg, summary, code, ok := v.isSuccessfulResponseFunc(r); ok {
		if arg == nil {
			// if a is nil, it means that there is no body
			v.responses[code] = model.NewRespWithoutBody(summary)
		} else {
			if code, t, err := v.deduceArgType(code, arg, v.groupwarePkg); err != nil {
				*v.errs = append(*v.errs, err)
				return nil
			} else {
				v.responses[code] = model.NewResp(t, summary, v.exampleKey, v.defaultValue)
			}
		}
	} else if ok := v.isErrorResponseFunc(r); ok {
		fmt.Printf("found error response func")
	}

	return v
}
