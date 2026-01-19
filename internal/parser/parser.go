package parser

import (
	"bytes"
	"encoding/json"
	"errors"
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
	"opencloud.eu/groupware-apidocs/internal/config"
	"opencloud.eu/groupware-apidocs/internal/model"
	"opencloud.eu/groupware-apidocs/internal/tools"
)

var (
	apiTag = regexp.MustCompile(`^\s*@api:tags?\s+(.+)\s*$`)

	tagAttrNameRegex      = regexp.MustCompile(`json:"(.+?)(?:,(omitempty|omitzero))?"`)
	tagDocRegex           = regexp.MustCompile(`doc:"(req|opt|)"`)
	tagNotInRequestRegex  = regexp.MustCompile(`doc:"(!request)"`)
	tagNotInResponseRegex = regexp.MustCompile(`doc:"(!response)"`)
)

func findRouteDefinition(a *ast.File) *ast.FuncDecl {
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

func endpoints(base string, stmts []ast.Stmt, pkg string) ([]model.Endpoint, error) {
	result := []model.Endpoint{}
	for _, stmt := range stmts {
		if call := stmtIsCallExpr(stmt); call != nil {
			if verb, err := methodNameOf(call, "r"); err == nil && verb != "" {
				if verb == "Route" {
					// recurse
					if path := stringArgOf(call, 0); path != "" {
						if deep := funcArgOf(call, 1); deep != nil {
							if deref, err := endpoints(join(base, path), deep.Body.List, pkg); err == nil {
								result = append(result, deref...)
							} else {
								return nil, err
							}
						}
					}
				} else if slices.Contains(config.Verbs, verb) && len(call.Args) == 2 {
					if path := stringArgOf(call, 0); path != "" {
						if f := funcRefArgOf(call, 1); f != nil {
							if fun, err := nameOf(f.Sel, pkg); err == nil {
								result = append(result, model.Endpoint{Verb: strings.ToUpper(verb), Path: join(base, path), Fun: fun})
							} else {
								return nil, err
							}
						}
					}
				} else if slices.Contains(config.CustomVerbs, verb) && len(call.Args) == 3 {
					if path := stringArgOf(call, 1); path != "" {
						if f := funcRefArgOf(call, 2); f != nil {
							if fun, err := nameOf(f.Sel, pkg); err == nil {
								result = append(result, model.Endpoint{Verb: strings.ToUpper(verb), Path: join(base, path), Fun: fun})
							} else {
								return nil, err
							}
						}
					}
				}
			} else if err != nil {
				return nil, err
			}
		}
	}
	return result, nil
}

type GlobalParamDefinitions struct {
	pathParams   map[string]model.ParamDefinition
	queryParams  map[string]model.ParamDefinition
	headerParams map[string]model.ParamDefinition
}

func scrapeGlobalParamDecls(decls []ast.Decl) (GlobalParamDefinitions, error) {
	pathParams := map[string]model.ParamDefinition{}
	queryParams := map[string]model.ParamDefinition{}
	headerParams := map[string]model.ParamDefinition{}
	for _, decl := range decls {
		if list, err := consts(decl); err != nil {
			return GlobalParamDefinitions{}, err
		} else {
			for _, c := range list {
				if tools.HasAnyPrefix(c.Name, config.PathParamPrefixes) {
					desc := describeParam(c.Comments)
					pathParams[c.Name] = model.NewParamDefinition(c.Value, desc)
				} else if tools.HasAnyPrefix(c.Name, config.QueryParamPrefixes) {
					desc := describeParam(c.Comments)
					queryParams[c.Name] = model.NewParamDefinition(c.Value, desc)
				} else if tools.HasAnyPrefix(c.Name, config.HeaderParamPrefixes) {
					desc := describeParam(c.Comments)
					headerParams[c.Name] = model.NewParamDefinition(c.Value, desc)
				}
			}
		}
	}
	return GlobalParamDefinitions{
		pathParams:   pathParams,
		queryParams:  queryParams,
		headerParams: headerParams,
	}, nil
}

func tags(_ string, path string, comments []string) []string {
	tags := []string{}

	for _, line := range comments {
		if m := apiTag.FindAllStringSubmatch(line, 1); m != nil {
			for tag := range strings.SplitSeq(m[0][1], ",") {
				tag = strings.TrimSpace(tag)
				if len(tag) > 0 {
					tags = append(tags, tag)
				}
			}
		}
	}

	// TODO if tags have been defined manually, should we autodiscover as well? currently assuming no.
	if len(tags) > 0 {
		return tags
	}

	obj := path
	if strings.HasPrefix(path, "/accounts/{accountid}/") {
		obj = path[len("/accounts/{accountid}"):]
	} else if strings.HasPrefix(path, "/accounts/all/") {
		obj = path[len("/accounts/all"):]
	}

	obj = strings.TrimPrefix(obj, "/")

	if len(obj) > 0 {
		if m := objsInObjById.FindAllStringSubmatch(obj, -1); m != nil {
			tags = append(tags, tools.Singularize(m[0][2]))
		} else {
			words := strings.Split(obj, "/")
			if len(words) > 0 {
				tags = append(tags, tools.Singularize(words[0]))
			}
		}
	}
	return tags
}

func decomment(str string) string {
	if cut, ok := strings.CutPrefix(str, "// "); ok {
		return cut
	}
	return strings.TrimPrefix(str, "//")
}

func describeParam(comments []string) string {
	if len(comments) < 1 {
		return ""
	}

	i := 0
	line := decomment(comments[i])
	for ; i < len(comments); i++ {
		if len(strings.TrimSpace(line)) >= 0 {
			break
		}
	}
	keep := []string{}
	for ; i < len(comments); i++ {
		if strings.HasPrefix(line, "@api:") {
			continue
		}
		keep = append(keep, line)
		line = decomment(comments[i])
	}
	return strings.Join(keep, "\n")
}

func skipEmptyLines(i int, lines []string) int {
	for ; i < len(lines); i++ {
		if !strings.HasPrefix(strings.TrimSpace(lines[i]), "swagger:") && len(strings.TrimSpace(lines[i])) > 0 {
			return i
		}
	}
	return -1
}

func summarizeType(comments []string) (string, string) {
	if len(comments) < 1 {
		return "", ""
	}

	i := 0
	i = skipEmptyLines(i, comments)
	if i < 0 {
		return "", ""
	}

	summary := comments[i]
	description := ""
	i++
	if i < len(comments) {
		i = skipEmptyLines(i, comments)
		if i >= 0 && i < len(comments) {
			description = strings.Join(comments[i:], "\n")
		}
	}

	summary = tools.Title(summary)
	description = tools.Title(description)

	return summary, description
}

var (
	objs              = regexp.MustCompile(`^([^/]+?s)/?$`)                                // e.g. 'emails' in /accounts/{accountid}/emails
	objById           = regexp.MustCompile(`^([^/]+?s)/{[^/]*?id}$`)                       // e.g. 'emails/{emailid}' in /accounts/all/emails/{emailid}
	objsInObjById     = regexp.MustCompile(`^([^/]+?s)/{[^/]*?id}/([^/]+?s)$`)             // e.g. 'mailboxes/{mailboxid}/roles' in /accounts/{accountid}/mailboxes/{mailboxid}/roles
	objsByIdInObjById = regexp.MustCompile(`^([^/]+?s)/{[^/]*?id}/([^/]+?s)//{[^/]*?id}$`) // e.g. 'mailboxes/{mailboxid}/roles/{roleid}' in /accounts/{accountid}/mailboxes/{mailboxid}/roles/{roleid}
)

func inferEndpointSummary(verb string, path string) (string, model.InferredSummary, error) {
	summary := ""

	forAccount := false
	forAllAccounts := false
	obj := path
	if strings.HasPrefix(path, "/accounts/{accountid}/") {
		forAccount = true
		obj = path[len("/accounts/{accountid}/"):]
	} else if strings.HasPrefix(path, "/accounts/all/") {
		forAccount = true
		forAllAccounts = true
		obj = path[len("/accounts/all/"):]
	} else {
		obj = path[1:] // remove the leading /
	}

	action := ""
	adjective := ""
	switch verb {
	case "GET":
		action = "retrieve"
		adjective = "retrieved"
	case "POST":
		action = "create"
		adjective = "created"
	case "PUT":
		action = "replace"
		adjective = "replaced"
	case "PATCH":
		action = "modify"
		adjective = "modified"
	case "DELETE":
		action = "delete"
		adjective = "deleted"
	default:
		return "", model.InferredSummary{}, fmt.Errorf("unsupported verb for endpoint summarization: '%s'", verb)
	}

	child := ""
	specificObj := false
	specificChild := false

	if m := objsByIdInObjById.FindAllStringSubmatch(obj, 2); m != nil {
		// e.g. 'mailboxes/{mailboxid}/roles/{roleid}' in /accounts/{accountid}/mailboxes/{mailboxid}/roles/{roleid}
		obj = tools.Singularize(m[0][1])
		child := tools.Singularize(m[0][2])
		specificObj = true
		specificChild = true
		// retrieve a {role} by its identifier of a {mailbox} by its own identifier
		summary = fmt.Sprintf("%s %s %s by its identifier of %s %s by its own identifier", action, tools.Article(child), child, tools.Article(obj), obj)
	} else if m := objsInObjById.FindAllStringSubmatch(obj, 2); m != nil {
		// e.g. 'mailboxes/{mailboxid}/roles' in /accounts/{accountid}/mailboxes/{mailboxid}/roles
		obj = tools.Singularize(m[0][1])
		child = m[0][2]
		specificObj = true
		// retrieve the {roles} of a {mailbox} by its identifier
		summary = fmt.Sprintf("%s the %s of %s %s by its identifier", action, child, tools.Article(obj), obj)
	} else if m := objById.FindAllStringSubmatch(obj, 1); m != nil {
		// e.g. 'emails/{emailid}' in /accounts/all/emails/{emailid}
		obj = tools.Singularize(m[0][1])
		specificObj = true
		// retrieve an {email} by its identifier
		summary = fmt.Sprintf("%s %s %s by its identifier", action, tools.Article(obj), obj)
	} else if m := objs.FindAllStringSubmatch(obj, 1); m != nil {
		// e.g. 'emails' in /accounts/{accountid}/emails
		obj = m[0][1]
		switch action {
		case "create":
			// create an {email}
			obj = tools.Singularize(obj)
			summary = fmt.Sprintf("%s %s %s", action, tools.Article(obj), obj)
		default:
			// retrieve all {emails}
			// delete all {emails}
			summary = fmt.Sprintf("%s all %s", action, obj)
		}
	} else {
		obj = ""
	}
	if summary != "" {
		if forAllAccounts {
			summary = summary + " for all accounts"
		} else if forAccount {
			summary = summary + " for a given account"
		}
	}

	return summary, model.InferredSummary{
		ForAccount:     forAccount,
		ForAllAccounts: forAllAccounts,
		Object:         obj,
		Child:          child,
		Action:         action,
		Adjective:      adjective,
		SpecificObject: specificObj,
		SpecificChild:  specificChild,
	}, nil
}

func summarizeEndpoint(verb string, path string, comments []string) (string, string, model.InferredSummary, error) {
	summary := ""
	description := ""
	if len(comments) > 0 {
		i := 0
		summary = decomment(comments[i])
		for i < len(comments) && len(strings.TrimSpace(summary)) == 0 { // skip empty lines at the beginning
			i++
			if i < len(comments) {
				summary = decomment(comments[i])
			}
		}
		if len(strings.TrimSpace(summary)) < 1 { // skip empty lines at the beginning
			i++
			summary = decomment(comments[i])
		}
		if strings.HasPrefix(summary, "swagger:route") {
			i++
			summary = decomment(comments[i])
		}
		for i++; i < len(comments); i++ {
			line := comments[i]
			trimmed := strings.TrimSpace(line)
			if len(trimmed) > 0 {
				break
			}
		}
		keep := []string{}
		for ; i < len(comments); i++ {
			line := decomment(comments[i])
			trimmed := strings.TrimSpace(line)
			if trimmed == "responses:" {
				break
			}
			if strings.HasPrefix(trimmed, "@api:") {
				continue
			}
			keep = append(keep, line)
		}
		description = strings.Join(keep, "\n")
	}

	inferredSummary, is, err := inferEndpointSummary(verb, path)
	if err != nil {
		return "", "", is, err
	}

	if summary == "" {
		summary = inferredSummary
	}

	summary = tools.Title(summary)
	description = tools.Title(description)

	summary = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(summary), "."))

	return summary, description, is, nil
}

type responseFunc struct {
	bodyArgPos int
	statusCode int
}

type returnVisitor struct {
	fset                    *token.FileSet
	typeMap                 map[string]model.Type
	groupwarePkg            *packages.Package
	responses               map[int]model.Resp
	successfulResponseFuncs map[string]responseFunc
	potentialErrors         map[string]bool
	undocumented            map[string]token.Position
	errs                    *[]error
}

func newReturnVisitor(fset *token.FileSet, groupwarePkg *packages.Package, typeMap map[string]model.Type, successfulResponseFuncs map[string]responseFunc) returnVisitor {
	return returnVisitor{
		fset:                    fset,
		groupwarePkg:            groupwarePkg,
		typeMap:                 typeMap,
		responses:               map[int]model.Resp{},
		potentialErrors:         map[string]bool{},
		successfulResponseFuncs: successfulResponseFuncs,
		undocumented:            map[string]token.Position{},
		errs:                    &[]error{},
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
			if t, ok := v.groupwarePkg.TypesInfo.Types[ident]; ok {
				if t.Type != nil {
					if n, ok := isNamed(t.Type); ok {
						if n.Obj() != nil && n.Obj().Name() == "Response" && n.Obj().Pkg() != nil && n.Obj().Pkg().Name() == "groupware" {
							switch decl := ident.Obj.Decl.(type) {
							case *ast.AssignStmt:
								for _, rhs := range decl.Rhs {
									if call, ok := isCallExpr(rhs); ok {
										if p, neededType, ok := isNeedAccountCall(call, v.groupwarePkg); ok && p.Required {
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
						summary = strings.Join(lines(cg.List), "\n") // TODO parse one-liner response function comment for api tags if needed
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
	r, err := typeOf(t, v.typeMap, v.groupwarePkg)
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

func (v returnVisitor) deduceArgType(code int, arg ast.Expr) (int, model.Type, error) {
	switch arg := arg.(type) {
	case *ast.SelectorExpr:
		key := arg.X.(*ast.Ident).Name + "." + arg.Sel.Name
		if m, ok := v.typeMap[key]; ok {
			return code, m, nil
		} else {
			fmt.Fprintf(os.Stderr, "failed to find the type '%s' in the typeMap, has the following types:\n", key)
			for _, k := range keysort(v.typeMap) {
				fmt.Fprintf(os.Stderr, "  - %s\n", k)
			}
			buf := new(bytes.Buffer)
			if err := ast.Fprint(buf, v.fset, arg, nil); err == nil {
				return 0, nil, fmt.Errorf("failed to find the type '%s' in the typeMap, used as the response type for %#v in %s", key, arg, buf.String())
			} else {
				return 0, nil, fmt.Errorf("failed to find the type '%s' in the typeMap, used as the response type for %#v", key, arg)
			}
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
						key = v.groupwarePkg.Name + "." + key
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
			v.responses[code] = model.Resp{Type: nil, Summary: summary}
		} else {
			if code, t, err := v.deduceArgType(code, arg); err != nil {
				*v.errs = append(*v.errs, err)
				return nil
			} else {
				v.responses[code] = model.Resp{Type: t, Summary: summary}
			}
		}
	} else if ok := v.isErrorResponseFunc(r); ok {

	}

	return v
}

type paramsVisitor struct {
	fset                      *token.FileSet
	fun                       string
	groupwarePkg              *packages.Package
	typeMap                   map[string]model.Type
	headerParams              map[string]model.Param
	queryParams               map[string]model.Param
	pathParams                map[string]model.Param
	bodyParams                map[string]model.Param
	responses                 map[int]model.Resp
	potentialErrors           map[string]bool
	responseFuncs             map[string]responseFunc
	undocumentedResults       map[string]token.Position
	undocumentedRequestBodies map[string]token.Position
	errs                      *[]error
}

func newParamsVisitor(fset *token.FileSet, groupwarePkg *packages.Package, fun string, typeMap map[string]model.Type, responseFuncs map[string]responseFunc) paramsVisitor {
	return paramsVisitor{
		fset:                      fset,
		fun:                       fun,
		groupwarePkg:              groupwarePkg,
		typeMap:                   typeMap,
		headerParams:              map[string]model.Param{},
		queryParams:               map[string]model.Param{},
		pathParams:                map[string]model.Param{},
		bodyParams:                map[string]model.Param{},
		responses:                 map[int]model.Resp{},
		potentialErrors:           map[string]bool{},
		responseFuncs:             responseFuncs,
		undocumentedResults:       map[string]token.Position{},
		undocumentedRequestBodies: map[string]token.Position{},
		errs:                      &[]error{},
	}
}

func (v paramsVisitor) isBodyCall(call *ast.CallExpr) (model.Param, bool, error) {
	if len(call.Args) != 1 && len(call.Args) != 2 {
		return model.Param{}, false, nil
	}

	_, _, funcName, ok := expandFQMethodCall(call, v.groupwarePkg, config.GroupwarePackageID, seqp("Request"), contp([]string{"body", "bodydoc"}))
	if !ok {
		return model.Param{}, false, nil
	}

	required := true  // TODO if needed, implement a way for body parameters to be optional; body parameters are required by default
	exploded := false // TODO body parameters typically aren't exploded since they are JSON payloads but implement something if needed
	var bodyArg ast.Expr
	var descArg ast.Expr
	if funcName == "body" && len(call.Args) == 1 {
		bodyArg = call.Args[0]
	} else if funcName == "bodydoc" && len(call.Args) == 2 {
		bodyArg = call.Args[0]
		descArg = call.Args[1]
	} else {
		return model.Param{}, false, nil
	}

	desc := ""
	if descArg != nil {
		if value, ok := isString(descArg); ok {
			desc = value
		} else {
			return model.Param{}, false, fmt.Errorf("unsupported call to Request.bodydoc(): description argument is not a BasicLit STRING but a STRING %T: %v", descArg, descArg)
		}
	}

	switch a := bodyArg.(type) {
	case *ast.UnaryExpr:
		if ident, ok := isIdent(a.X); ok && a.Op == token.AND && ident.Obj != nil {
			if vs, ok := isValueSpec(ident.Obj.Decl); ok {
				if n, err := nameOf(vs.Type, v.groupwarePkg.Name); err == nil {
					if typ, err := toType(n, v.typeMap); err != nil {
						return model.Param{}, false, fmt.Errorf("failed to resolve type identifier '%s' from call to Request.body[doc]()", n)
					} else {
						return model.NewParam(n, desc, typ, required, exploded), true, nil
					}
				} else {
					return model.Param{}, false, err
				}
			} else {
				return model.Param{}, false, fmt.Errorf("unsupported call to Request.body[doc](): UnaryExpr argument is not an Ident but a %v", a)
			}
		} else {
			return model.Param{}, false, fmt.Errorf("unsupported call to Request.body[doc](): UnaryExpr X is not an Ident but a %T: %v", a.X, a.X)
		}
	default:
		return model.Param{}, false, fmt.Errorf("unsupported call to Request.body[doc](): argument is not a UnaryExpr but a %T: %v", a, a)
	}
}

func (v paramsVisitor) isRespondCall(call *ast.CallExpr) (map[int]model.Resp, map[string]bool, map[string]token.Position, bool, error) {
	if len(call.Args) == 3 && isMethodCall(call, "Groupware", "respond") {
		arg := call.Args[2]
		if c, ok := isClosure(arg); ok {
			rv := newReturnVisitor(v.fset, v.groupwarePkg, v.typeMap, v.responseFuncs)
			ast.Walk(rv, c)
			if err := errors.Join(*rv.errs...); err != nil {
				return nil, nil, nil, false, err
			}
			if len(rv.responses) > 0 {
				return rv.responses, rv.potentialErrors, rv.undocumented, true, nil
			}
		}
	}
	return nil, nil, nil, false, nil
}

func isAccountCall(call *ast.CallExpr, p *packages.Package) (model.Param, bool) {
	if len(call.Args) == 0 && isMethodCallPrefix(call, "Request", []string{"GetAccountFor", "GetAccountIdFor"}) {
		required := true
		exploded := false
		summary := ""
		if cg := findInlineComment(call.Pos(), p); cg != nil && cg.List != nil {
			summary = strings.Join(lines(cg.List), "\n")
		}
		return model.NewParam(config.AccountIdUriParamName, summary, model.StringType, required, exploded), true
	} else {
		return model.Param{}, false
	}
}

var needObjectWithAccountCallRegex = regexp.MustCompile(`^need(Contact|Calendar|Task)WithAccount$`)

func isNeedAccountCall(call *ast.CallExpr, p *packages.Package) (model.Param, string, bool) {
	required := true
	exploded := false
	if s, ok := isSelector(call.Fun); ok {
		if x, ok := isIdent(s.X); ok && len(call.Args) == 0 && x.Obj != nil {
			if m := needObjectWithAccountCallRegex.FindAllStringSubmatch(s.Sel.Name, 1); m != nil {
				if f, ok := isField(x.Obj.Decl); ok {
					if t, ok := isIdent(f.Type); ok && t.Name == "Request" {
						summary := ""
						if cg := findInlineComment(call.Pos(), p); cg != nil && cg.List != nil {
							summary = strings.Join(lines(cg.List), "\n")
						}
						return model.NewParam(config.AccountIdUriParamName, summary, model.StringType, required, exploded), m[0][1], true
					}
				}
			}
		}
	}
	return model.Param{}, "", false
}

var (
	parseOptionalParamCallRegex          = regexp.MustCompile(`^(?:parse|get)([A-Z].*?)Param$`)
	parseMandatoryParamCallRegex         = regexp.MustCompile(`^(?:parse|get)Mandatory([A-Z].*?)Param$`)
	parseOptionalSingleArgParamCallRegex = regexp.MustCompile(`^parseOpt([A-Z].*?)Param$`)
)

func (v paramsVisitor) isParseQueryParamCall(call *ast.CallExpr) (model.Param, bool, error) {
	typeDef := ""
	summary := ""
	required := false
	var nameArg ast.Expr
	if m := isMethodCallRegex(call, "Request", parseOptionalSingleArgParamCallRegex, 1); m != nil {
		typeDef = m[0][1]
		required = false
		nameArg = call.Args[0]
	} else if m := isMethodCallRegex(call, "Request", parseMandatoryParamCallRegex, 1); m != nil {
		typeDef = m[0][1]
		required = true
		nameArg = call.Args[0]
	} else if m := isMethodCallRegex(call, "Request", parseOptionalParamCallRegex, 1); m != nil {
		typeDef = m[0][1]
		required = false
		nameArg = call.Args[0]
	}

	name := ""
	if a, ok := isIdent(nameArg); ok {
		name = a.Name
	} else {
		return model.Param{}, false, nil
	}

	exploded := false
	var typ model.Type = nil
	if typeDef != "" {
		switch typeDef {
		case "String":
			typ = model.StringType
		case "Int":
			typ = model.IntType
		case "UInt":
			typ = model.UIntType
		case "Date":
			typ = model.StringType
		case "Bool":
			typ = model.BoolType
		case "StringList":
			typ = model.NewArrayType(model.StringType)
			exploded = true
		case "Map":
			typ = model.NewMapType(model.StringType, model.StringType)
		default:
			return model.Param{}, true, fmt.Errorf("unsupported type '%s' for query parameter through call to 'Request'", typeDef)
		}
	} else if len(call.Args) == 1 && tools.HasAnyPrefix(name, config.QueryParamPrefixes) &&
		isFQMethodCall(call, v.groupwarePkg, "net/url", seqp("Values"), seqp("Get")) {
		required = false
		typ = model.StringType
	} else {
		return model.Param{}, false, nil
	}

	if summary == "" {
		if cg := findInlineComment(call.Pos(), v.groupwarePkg); cg != nil && len(cg.List) > 0 {
			summary = strings.Join(lines(cg.List), "\n") // TODO parse one-liner response function comment for api tags if needed
		}
	}

	// TODO resolve name (QueryParamXYZ) to its string value using model.QueryParams map

	return model.NewParam(name, summary, typ, required, exploded), true, nil

}

func (v paramsVisitor) isPathParamCall(call *ast.CallExpr) (model.Param, bool) {
	required := true
	if len(call.Args) == 2 && isStaticFunc(call, "chi", "URLParam") {
		if a, ok := isIdent(call.Args[1]); ok {
			summary := ""
			if cg := findInlineComment(call.Pos(), v.groupwarePkg); cg != nil && len(cg.List) > 0 {
				summary = strings.Join(lines(cg.List), "\n") // TODO parse one-liner response function comment for api tags if needed
			}
			return model.NewParam(a.Name, summary, model.StringType, required, false), true
		} else {
			panic(fmt.Errorf("chi.URLParam first argument is not an Ident but a %T: %v", call.Args[0], call.Args[0]))
		}
	} else if _, _, f, ok := expandFQMethodCall(call, v.groupwarePkg, config.GroupwarePackageID, seqp("Request"), contp([]string{"PathParam", "PathParamDoc", "PathListParamDoc"})); ok {
		if f == "PathParam" && len(call.Args) == 1 {
			if a, ok := isIdent(call.Args[0]); ok {
				summary := ""
				if cg := findInlineComment(call.Pos(), v.groupwarePkg); cg != nil && len(cg.List) > 0 {
					summary = strings.Join(lines(cg.List), "\n") // TODO parse one-liner response function comment for api tags if needed
				}
				return model.NewParam(a.Name, summary, model.StringType, required, false), true
			} else {
				panic(fmt.Errorf("Request.%s first argument is not an Ident but a %T: %v", f, call.Args[0], call.Args[0]))
			}
		} else if len(call.Args) == 2 {
			var typ model.Type = model.StringType
			if strings.HasPrefix(f, "PathList") {
				typ = model.NewArrayType(model.StringType)
			}
			if a, ok := isIdent(call.Args[0]); ok {
				if desc, ok := isString(call.Args[1]); ok {
					return model.NewParam(a.Name, desc, typ, required, true), true
				} else {
					panic(fmt.Errorf("Request.%s second argument is not a BasicLit STRING but a %T: %v", f, call.Args[1], call.Args[1]))
				}
			} else {
				panic(fmt.Errorf("Request.%s first argument is not an Ident but a %T: %v", f, call.Args[0], call.Args[0]))
			}
		}
	}
	// TODO PathParam methods that also cast to a different type (int, ...) than string, if needed
	// TODO optional PathParam methods where req=NotRequired, if needed (although path params should always be required, in theory)
	return model.Param{}, false
}

func (v paramsVisitor) isHeaderParamCall(call *ast.CallExpr) (model.Param, bool) {
	if len(call.Args) == 2 && isFQMethodCall(call, v.groupwarePkg, config.GroupwarePackageID, seqp("Request"), seqp("HeaderParamDoc")) {
		if a, ok := isIdent(call.Args[0]); ok {
			if summary, ok := isString(call.Args[1]); ok {
				return model.NewParam(a.Name, summary, model.StringType, true, false), true
			}
		}
	} else if len(call.Args) == 1 && isFQMethodCall(call, v.groupwarePkg, config.GroupwarePackageID, seqp("Request"), seqp("HeaderParam")) {
		if a, ok := isIdent(call.Args[0]); ok {
			summary := ""
			if cg := findInlineComment(call.Pos(), v.groupwarePkg); cg != nil && len(cg.List) > 0 {
				summary = strings.Join(lines(cg.List), "\n") // TODO parse one-liner response function comment for api tags if needed
			}
			return model.NewParam(a.Name, summary, model.StringType, true, false), true
		}
	} else if len(call.Args) == 2 && isFQMethodCall(call, v.groupwarePkg, config.GroupwarePackageID, seqp("Request"), seqp("OptHeaderParamDoc")) {
		if a, ok := isIdent(call.Args[0]); ok {
			if summary, ok := isString(call.Args[1]); ok {
				return model.NewParam(a.Name, summary, model.StringType, false, false), true
			}
		}
	} else if len(call.Args) == 1 && isFQMethodCall(call, v.groupwarePkg, config.GroupwarePackageID, seqp("Request"), seqp("OptHeaderParam")) {
		if a, ok := isIdent(call.Args[0]); ok {
			summary := ""
			if cg := findInlineComment(call.Pos(), v.groupwarePkg); cg != nil && len(cg.List) > 0 {
				summary = strings.Join(lines(cg.List), "\n") // TODO parse one-liner response function comment for api tags if needed
			}
			return model.NewParam(a.Name, summary, model.StringType, false, false), true
		}
	}
	return model.Param{}, false
}

func (v paramsVisitor) isBuildFilterCall(call *ast.CallExpr) (map[string]model.Param, map[string]model.Param, error) {
	if len(call.Args) != 1 {
		return nil, nil, nil
	}
	if _, ok := isSelector(call.Fun); !ok {
		return nil, nil, nil
	}
	if !isFQMethodCall(call, v.groupwarePkg, config.GroupwarePackageID, seqp("Groupware"), seqp("buildEmailFilter")) {
		return nil, nil, nil
	}

	var bf *ast.FuncDecl
	{
		for _, syn := range v.groupwarePkg.Syntax {
			for _, decl := range syn.Decls {
				if f := isFQFuncDecl(decl, v.groupwarePkg, "Groupware", "buildEmailFilter"); f != nil {
					bf = f
					break
				}
			}
		}
	}
	if bf == nil {
		return nil, nil, fmt.Errorf("failed to find declaration of 'buildEmailFilter' in the package '%s'", v.groupwarePkg.ID)
	}

	p := newParamsVisitor(v.fset, v.groupwarePkg, bf.Name.Name, v.typeMap, v.responseFuncs)
	ast.Walk(p, bf.Body)
	if err := errors.Join(*v.errs...); err != nil {
		return nil, nil, err
	}

	return p.queryParams, p.headerParams, nil
}

func (v paramsVisitor) Visit(n ast.Node) ast.Visitor {
	if n == nil { // = nothing left to visit
		return nil
	}
	switch d := n.(type) {
	/*
		use this to catch any reference to a QueryParam* or UriParam* constant within the body of a method:

		case *ast.Ident:
			if hasAnyPrefix(d.Name, QueryParamPrefixes) {
				v.queryParams[d.Name] = true
			} else if hasAnyPrefix(d.Name, PathParamPrefixes) {
				v.pathParams[d.Name] = Param{Name: d.Name, Description: "", Required: true}
			}
	*/
	case *ast.CallExpr:
		if p, ok, err := v.isBodyCall(d); err != nil {
			*v.errs = append(*v.errs, err)
			return nil
		} else if ok {
			v.bodyParams[p.Name] = p
			if p.Description == "" {
				pos := v.fset.Position(d.Pos())
				v.undocumentedRequestBodies[p.Name] = pos
			}
			return v
		}

		if p, ok := isAccountCall(d, v.groupwarePkg); ok {
			v.pathParams[p.Name] = p
			return v
		}
		if responses, potentialErrors, undocumented, ok, err := v.isRespondCall(d); err != nil {
			*v.errs = append(*v.errs, err)
			return nil
		} else if ok {
			maps.Copy(v.responses, responses)
			maps.Copy(v.potentialErrors, potentialErrors)
			maps.Copy(v.undocumentedResults, undocumented)
			return v
		}
		if p, _, ok := isNeedAccountCall(d, v.groupwarePkg); ok {
			v.pathParams[p.Name] = p
		}
		if p, ok := v.isPathParamCall(d); ok {
			v.pathParams[p.Name] = p
			return v
		}

		if p, ok, err := v.isParseQueryParamCall(d); err != nil {
			*v.errs = append(*v.errs, err)
			return nil
		} else if ok {
			v.queryParams[p.Name] = p
			return v
		}

		if p, ok := v.isHeaderParamCall(d); ok {
			v.headerParams[p.Name] = p
			return v
		}

		if queryParams, headerParams, err := v.isBuildFilterCall(d); err != nil {
			*v.errs = append(*v.errs, err)
			return nil
		} else {
			maps.Copy(v.queryParams, queryParams)
			maps.Copy(v.headerParams, headerParams)
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
	if slices.Contains(config.MiddlewareFunctionNames, f.Name()) {
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

func findFun(n string, p *packages.Package, _ *types.Func) (*ast.FuncDecl, string, bool) {
	for i, f := range p.CompiledGoFiles {
		s := p.Syntax[i] // TODO doesn't necessarily fit, can have nils that are compacted away
		if s == nil {
			continue
		}
		for _, d := range s.Decls {
			switch x := d.(type) {
			case *ast.FuncDecl:
				fname := s.Name.Name + "." + x.Name.Name
				if n == fname {
					return x, f, true
				}
				/*
					if fun.Pos() <= x.Pos() && x.End() >= fun.Pos() {
						return x, f, true
					}
				*/
			}
		}
	}
	return nil, "", false
}

func findTopDecl(_ string, t token.Pos, p *packages.Package) ast.Decl {
	pos := p.Fset.Position(t)
	for _, s := range p.Syntax {
		fp := p.Fset.Position(s.FileStart)
		if fp.Filename != pos.Filename {
			continue
		}
		for _, d := range s.Decls {
			dp := p.Fset.Position(d.Pos())
			if pos.Line == dp.Line {
				return d
			}
		}
	}
	return nil
}

func findInlineComment(t token.Pos, p *packages.Package) *ast.CommentGroup {
	pos := p.Fset.Position(t)
	for _, s := range p.Syntax {
		fp := p.Fset.Position(s.FileStart)
		if fp.Filename != pos.Filename {
			continue
		}
		for _, g := range s.Comments {
			gp := p.Fset.Position(g.Pos())
			if pos.Line == gp.Line {
				return g
			}
		}
	}
	return nil
}

func findCommentGroup(_ string, t token.Pos, p *packages.Package) *ast.CommentGroup {
	pos := p.Fset.Position(t)
	for _, s := range p.Syntax {
		fp := p.Fset.Position(s.FileStart)
		if fp.Filename != pos.Filename {
			continue
		}
		for _, g := range s.Comments {
			gp := p.Fset.Position(g.Pos())
			ge := p.Fset.Position(g.End())
			if pos.Line == gp.Line || ge.Line == pos.Line-1 {
				return g
			}
		}
	}
	return nil
}

func findComments(name string, pos token.Pos, tokenType token.Token, pkg *packages.Package) []string {
	d := findTopDecl(name, pos, pkg)
	if d != nil {
		switch x := d.(type) {
		case *ast.GenDecl:
			if x.Tok == tokenType {
				if x.Doc != nil {
					return lines(x.Doc.List)
				} else {
					return []string{}
				}
			}
		}
	}
	c := findCommentGroup(name, pos, pkg)
	if c != nil {
		return lines(c.List)
	}
	return []string{}
}

func lines(s []*ast.Comment) []string {
	return tools.Collect(s, func(c *ast.Comment) string { return decomment(c.Text) })
}

func Parse(chdir string, basepath string) (model.Model, error) {
	httpStatusMap := map[string]int{}
	{
		cfg := &packages.Config{
			Mode:  packages.LoadSyntax,
			Tests: false,
		}
		pkgs, err := packages.Load(cfg, config.NetHttpPackageID)
		if err != nil {
			log.Fatal(err)
		}
		if packages.PrintErrors(pkgs) > 0 {
			return model.Model{}, fmt.Errorf("failed to parse the package '%s'", config.NetHttpPackageID)
		}
		var httpPackage *packages.Package = nil
		{
			for _, p := range pkgs {
				if p.ID == config.NetHttpPackageID {
					httpPackage = p
					break
				}
			}
			if httpPackage == nil {
				panic(fmt.Errorf("failed to find the package '%s'", config.NetHttpPackageID))
			}
		}
		httpStatusMap = parseHttpStatuses(httpPackage)
	}

	routeFuncs := map[string]*types.Func{}
	typeMap := map[string]model.Type{}
	constsMap := map[string]bool{}
	groupwareErrorCodesMap := map[string]string{}
	groupwareErrorsMap := map[string]model.PotentialError{}
	routes := []model.Endpoint{}
	pathParams := map[string]model.ParamDefinition{}
	queryParams := map[string]model.ParamDefinition{}
	headerParams := map[string]model.ParamDefinition{}
	ims := []model.Impl{}
	undocumentedResults := map[string]model.Undocumented{}
	undocumentedResultBodies := map[string]model.Undocumented{}
	{
		cfg := &packages.Config{
			Mode:  packages.LoadSyntax,
			Dir:   chdir,
			Tests: false,
		}
		pkgs, err := packages.Load(cfg, config.SourceDirectories...)
		if err != nil {
			log.Fatal(err)
		}
		if packages.PrintErrors(pkgs) > 0 {
			return model.Model{}, fmt.Errorf("failed to compile Go source code of one of the following directories: %s", strings.Join(config.SourceDirectories, ", "))
		}

		// TODO extract the response funcs from the source code: look for functions in the groupware package that return a Response object, and look for the "body" parameter
		responseFuncs := map[string]responseFunc{
			"etagResponse":              {1, http.StatusOK},
			"response":                  {1, http.StatusOK},
			"noContentResponse":         {-1, http.StatusNoContent},
			"noContentResponseWithEtag": {-1, http.StatusNoContent},
			"notFoundResponse":          {-1, http.StatusNotFound},
			"etagNotFoundResponse":      {-1, http.StatusNotFound},
			"notImplementedResponse":    {-1, http.StatusNotImplemented},
		}

		// iterate over every package that we are interested in (defined in config.PackageIDs),
		// and analyze types and constants in each of them
		for _, p := range pkgs {
			if !slices.Contains(config.PackageIDs, p.ID) {
				continue // we're not interested in that package
			}

			for _, name := range p.Types.Scope().Names() {
				obj := p.Types.Scope().Lookup(name)
				switch t := obj.Type().(type) {
				case *types.Signature:
					// skip methods
				case *types.Named:
					// this is a globally defined type
					if r, err := typeOf(t, typeMap, p); err != nil {
						log.Panicf("failed to determine type of named %#v: %v", t, err)
					} else if r != nil {
						typeMap[r.Key()] = r
					}
				case *types.Basic:
					switch t.Kind() {
					case types.UntypedString, types.String:
						// a const string, we want to collect those for detecting enums
						constsMap[name] = true
					case types.UntypedInt, types.Int, types.UntypedBool, types.Bool:
						// ignore
					default:
						log.Panicf("%s is Basic but not string: %s", name, t.Name())
					}
				case *types.Slice:
					//fmt.Printf("globvar(slice): %s: %s\n", name, t.String())
				case *types.Map:
					//fmt.Printf("globvar(map): %s: %s\n", name, t.String())
				case *types.Pointer:
					//fmt.Printf("globvar(ptr): %s: %s\n", name, t.String())
				default:
					log.Panicf("failed to analyze type %s: is not a Named but a %T", name, t)
				}
			}
		}

		// find the Package for "groupware"
		var groupware *packages.Package = nil
		{
			for _, p := range pkgs {
				if p.ID == config.GroupwarePackageID {
					groupware = p
					break
				}
			}
			if groupware == nil {
				panic("failed to find the groupware package " + config.GroupwarePackageID)
			}
		}

		// fill routeFuncs
		{
			for _, d := range groupware.TypesInfo.Defs {
				if d == nil {
					continue
				}
				if f, ok := d.(*types.Func); ok && isRouteFun(f) {
					routeFuncs[groupware.Name+"."+f.Name()] = f
				}
			}
		}

		// analyze routes, route functions and definitions of path and query parameter name
		// constants in groupware_route.go
		{
			var syntax *ast.File = nil
			{
				for i, f := range groupware.CompiledGoFiles {
					if rf, err := filepath.Rel(basepath, f); err != nil {
						panic(err)
					} else if rf == "services/groupware/pkg/groupware/groupware_route.go" {
						syntax = groupware.Syntax[i]
						break
					}
				}
				if syntax == nil {
					panic("failed to find syntax for groupware_route.go")
				}
			}

			routeFunc := findRouteDefinition(syntax)
			if routeFunc == nil {
				log.Fatal("failed to find Route() method")
			}

			{
				if r, err := endpoints("/", routeFunc.Body.List, groupware.Name); err != nil {
					panic(err)
				} else {
					routes = append(routes, r...)
				}
			}
			{
				if g, err := scrapeGlobalParamDecls(syntax.Decls); err != nil {
					panic(err)
				} else {
					maps.Copy(pathParams, g.pathParams)
					maps.Copy(queryParams, g.queryParams)
					maps.Copy(headerParams, g.headerParams)
				}
			}
		}

		{
			var syntax *ast.File = nil
			{
				for i, f := range groupware.CompiledGoFiles {
					if rf, err := filepath.Rel(basepath, f); err != nil {
						panic(err)
					} else if rf == "services/groupware/pkg/groupware/groupware_error.go" {
						syntax = groupware.Syntax[i]
						break
					}
				}
				if syntax == nil {
					panic("failed to find syntax for groupware_error.go")
				}
			}

			for _, decl := range syntax.Decls {
				if v, ok := isGenDecl(decl); ok && len(v.Specs) > 0 && v.Tok == token.CONST {
					for _, s := range v.Specs {
						if m, err := findGroupwareErrorCodeDefinitions(s); err != nil {
							panic(err)
						} else {
							maps.Copy(groupwareErrorCodesMap, m)
						}
					}
				}
			}

			if groupwareErrorType, ok := typeMap["groupware.GroupwareError"]; ok {
				for _, decl := range syntax.Decls {
					if v, ok := isGenDecl(decl); ok && len(v.Specs) > 0 && v.Tok == token.VAR {
						for _, s := range v.Specs {
							if m, err := findGroupwareErrorDefinitions(s, groupwareErrorType, groupwareErrorCodesMap, httpStatusMap); err != nil {
								panic(err)
							} else {
								maps.Copy(groupwareErrorsMap, m)
							}
						}
					}
				}
			} else {
				panic(fmt.Errorf("failed to find 'groupware.GroupwareError' in the typeMap"))
			}
		}

		for n, f := range routeFuncs {
			if fun, source, ok := findFun(n, groupware, f); !ok {
				log.Panicf("failed to find function declaration for route function '%s' in package '%s'", n, groupware.Name)
			} else {
				comments := []string{}
				if fun.Doc != nil && len(fun.Doc.List) > 0 {
					for _, doc := range fun.Doc.List {
						comments = append(comments, parseComment(doc.Text))
					}
				}

				var route model.Endpoint
				{
					foundRoute := false
					for _, r := range routes {
						if r.Fun == n {
							route = r
							foundRoute = true
							break
						}
					}
					if !foundRoute {
						log.Panicf("failed to find endpoint for route function '%s'", n)
					}
				}

				resp := map[int]model.Resp{}
				bodyParams := map[string]model.Param{}
				headerParams := map[string]model.Param{}
				pathParams := map[string]model.Param{}
				queryParams := map[string]model.Param{}
				potentialErrors := map[string]model.PotentialError{}
				{
					if fun.Body != nil {
						v := newParamsVisitor(groupware.Fset, groupware, n, typeMap, responseFuncs)
						ast.Walk(v, fun.Body)
						if err := errors.Join(*v.errs...); err != nil {
							panic(err)
						}
						maps.Copy(resp, v.responses)
						maps.Copy(bodyParams, v.bodyParams)
						maps.Copy(headerParams, v.headerParams)
						maps.Copy(pathParams, v.pathParams)
						maps.Copy(queryParams, v.queryParams)

						for k, pos := range v.undocumentedResults {
							undocumentedResults[k] = model.Undocumented{
								Pos:      pos,
								Endpoint: route,
							}
						}
						for k, pos := range v.undocumentedRequestBodies {
							undocumentedResultBodies[k] = model.Undocumented{
								Pos:      pos,
								Endpoint: route,
							}
						}

						for k := range v.potentialErrors {
							if t, ok := groupwareErrorsMap[k]; ok {
								potentialErrors[k] = t
							} else {
								panic(fmt.Errorf("failed to find potential error type key '%s' in groupwareErrorsMap", k))
							}
						}
					}
				}

				source, err := filepath.Rel(basepath, source)
				if err != nil {
					panic(err)
				}

				position := groupware.Fset.Position(fun.Pos())
				filename := position.Filename
				if relpath, err := filepath.Rel(basepath, filename); err != nil {
					panic(err)
				} else {
					filename = relpath
				}

				tags := tags(route.Verb, route.Path, comments)
				summary, description, inferredSummary, err := summarizeEndpoint(route.Verb, route.Path, comments)
				if err != nil {
					log.Panicf("%s %s: failed to summarize the endpoint for route function '%s' in package '%s'", route.Verb, route.Path, n, groupware.Name)
				}

				ims = append(ims, model.Impl{
					Endpoint:        route,
					Name:            n,
					Fun:             fun,
					Source:          source,
					Filename:        filename,
					Line:            position.Line,
					Column:          position.Column,
					Comments:        comments,
					Resp:            resp,
					QueryParams:     valuesOf(queryParams),
					PathParams:      valuesOf(pathParams),
					HeaderParams:    valuesOf(headerParams),
					BodyParams:      valuesOf(bodyParams),
					PotentialErrors: potentialErrors,
					Tags:            tags,
					Summary:         summary,
					Description:     description,
					InferredSummary: inferredSummary,
				})

			}
		}
	}

	exampleMap := map[string]model.Examples{}
	{
		m := map[string]map[string][]model.Example{}

		type example struct {
			Type    string          `json:"type"`
			Key     string          `json:"key"`
			Title   string          `json:"title"`
			Scope   string          `json:"scope"`
			Origin  string          `json:"origin"`
			Example json.RawMessage `json:"example"`
		}

		for _, d := range config.SourceDirectories {
			p := filepath.Join(chdir, d)
			f := "apidoc-examples.json"
			{
				qf := filepath.Join(p, f)
				if _, err := os.Stat(qf); err != nil {
					if !errors.Is(err, os.ErrNotExist) {
						panic(err)
					} else {
						continue
					}
				}

				if b, err := os.ReadFile(qf); err != nil {
					panic(err)
				} else {
					all := []example{}
					if err := json.Unmarshal(b, &all); err != nil {
						panic(err)
					} else {
						for _, v := range all {
							k := v.Type
							q := v.Scope
							if q == "" {
								q = "any"
							}

							title := v.Title
							if title == "" {
								n := v.Type
								if i := strings.Index(n, "."); i >= 0 {
									n = n[i+1:]
								}
								title = tools.Title(fmt.Sprintf("%s %s", tools.Article(n), n))
							}

							if b, err := v.Example.MarshalJSON(); err != nil {
								panic(err)
							} else {
								if _, ok := m[k]; !ok {
									m[k] = map[string][]model.Example{}
								}
								list, ok := m[k][q]
								if !ok {
									list = []model.Example{}
								}
								origin := filepath.Join(d, f)
								if v.Origin != "" {
									origin = origin + ":" + v.Origin
								}
								list = append(list, model.Example{
									Key:    v.Key,
									TypeId: v.Type,
									Title:  title,
									Text:   string(b),
									Origin: origin,
								})
								m[k][q] = list
							}
						}
					}
				}
			}
		}

		for k, qualified := range m {
			e := model.Examples{
				Key: k,
			}
			remaining := maps.Clone(qualified)
			if x, ok := qualified["any"]; ok {
				delete(remaining, "any")
				e.DefaultExamples = append(e.DefaultExamples, x...)
			} else {
				panic(fmt.Errorf("no default example for %s", k))
			}
			if x, ok := qualified["request"]; ok {
				delete(remaining, "request")
				e.RequestExamples = append(e.RequestExamples, x...)
			}
			if len(remaining) > 0 {
				panic(fmt.Errorf("unsupported qualifiers found for examples for '%s': %s", k, strings.Join(slices.Collect(maps.Keys(remaining)), ", ")))
			}
			exampleMap[k] = e
		}
	}

	enums := map[string][]string{} // TODO implement enums

	defaultResponses := map[int]model.DefaultResponseDesc{}
	{
		if t, ok := typeMap["groupware.ErrorResponse"]; ok {
			for _, statusCode := range []int{400, 404, 500} {
				summary := tools.MustHttpStatusText(statusCode) // TODO describe default responses
				defaultResponses[statusCode] = model.DefaultResponseDesc{
					Type:    t,
					Summary: summary,
				}
			}
		}
	}

	// TODO extract default headers and their documentation from the source code (groupware_framework.go)
	defaultResponseHeaders := map[string]model.DefaultResponseHeaderDesc{}
	{
		// defaultHeaders["Content-Language"]
		// defaultHeaders["ETag"]
		defaultResponseHeaders["Session-State"] = model.DefaultResponseHeaderDesc{
			Summary:  "The opaque state identifier for the JMAP Session",
			Required: true,
			Examples: map[string]string{"a JMAP Session identifier": "eish5Toh"},
		}
		defaultResponseHeaders["State"] = model.DefaultResponseHeaderDesc{
			Summary:   "The opaque state identifier for the type of objects in the response (see the `Object-Type` header)",
			OnError:   false,
			OnSuccess: true,
			Examples:  map[string]string{"a State identifier": "ra8eey5T"},
		}
		defaultResponseHeaders["Object-Type"] = model.DefaultResponseHeaderDesc{
			Summary:   "The type of JMAP objects returned in the response",
			OnError:   true,
			OnSuccess: true,
			Examples: map[string]string{
				"the State pertains to Identities":         "identity",
				"the State pertains to Emails":             "email",
				"the State pertains to Contacts":           "contact",
				"the State pertains to Addressbooks":       "addressbook",
				"the State pertains to Quotas":             "quota",
				"the State pertains to Vacation Responses": "vacationresponse",
			},
		}
		defaultResponseHeaders["Account-Id"] = model.DefaultResponseHeaderDesc{
			Summary:   "The identifier of the account the operation was performed against, when against a single account",
			OnError:   true,
			OnSuccess: true,
			Examples:  map[string]string{"an Account identifier": "b"},
		}
		defaultResponseHeaders["Account-Ids"] = model.DefaultResponseHeaderDesc{
			Summary:   "The identifier of the accounts the operation was performed against, when against multiple accounts",
			Explode:   true,
			OnError:   true,
			OnSuccess: true,
			Examples:  map[string]string{"a list of Account identifiers": "b,d,e"},
		}
		defaultResponseHeaders["Trace-Id"] = model.DefaultResponseHeaderDesc{
			Summary:   "The value of the Trace-Id header that was specified in the request or, if not, a unique randomly generated identifier that is included in logging output",
			Required:  true,
			OnError:   true,
			OnSuccess: true,
			Examples:  map[string]string{"a UUID as trace ID": "4ab74941-b178-4565-9e28-2bb35eb2ff0c"},
		}
		defaultResponseHeaders["Unmatched-Path"] = model.DefaultResponseHeaderDesc{
			Summary:   "When the requested path does not pertain to any existing API, this header is set to differentiate from cases where a referenced object could not be found",
			Required:  false,
			OnError:   true,
			OnSuccess: false,
			OnlyCodes: []int{404},
			Examples:  map[string]string{"an API path that does not exist": "/accounts/{accountid}/cars"},
		}
		defaultResponseHeaders["Unsupported-Method"] = model.DefaultResponseHeaderDesc{
			Summary:   "When the requested path exists but the method is not supported for that path, this header is set to differentiate from cases where a referenced object could not be found",
			Required:  false,
			OnError:   true,
			OnSuccess: false,
			OnlyCodes: []int{404},
			Examples:  map[string]string{"a method that does not exist for an API path that does exist": "OPTIONS"},
		}
	}

	commonRequestHeaders := []model.RequestHeaderDesc{}
	{
		// TODO retrieve common request headers from the source code
		commonRequestHeaders = append(commonRequestHeaders, model.RequestHeaderDesc{
			Name:        "X-Request-ID",
			Description: "When specified, its value is used in logs for correlation",
			Required:    false,
			Exploded:    false,
			Examples: map[string]string{
				"A UUID as request ID": "433bfe7b-032a-4ec3-8fb9-87ee5fb97af7",
			},
		})
		commonRequestHeaders = append(commonRequestHeaders, model.RequestHeaderDesc{
			Name:        "Trace-Id",
			Description: "When specified, its value is used in logs for correlation and if not, a new random value is generated and sent in the response",
			Required:    false,
			Exploded:    false,
			Examples: map[string]string{
				"A UUID as trace ID": "4ab74941-b178-4565-9e28-2bb35eb2ff0c",
			},
		})
	}

	types := slices.Collect(maps.Values(typeMap))

	return model.Model{
		Routes:                              routes,
		PathParams:                          pathParams,
		QueryParams:                         queryParams,
		HeaderParams:                        headerParams,
		Impls:                               ims,
		Types:                               types,
		Examples:                            exampleMap,
		Enums:                               enums,
		DefaultResponses:                    defaultResponses,
		DefaultResponseHeaders:              defaultResponseHeaders,
		CommonRequestHeaders:                commonRequestHeaders,
		UndocumentedResults:                 undocumentedResults,
		UndocumentedRequestBodies:           undocumentedResultBodies,
		GlobalPotentialErrors:               mustMapPotentialErrors(GlobalPotentialErrors, groupwareErrorsMap),
		GlobalPotentialErrorsForQueryParams: mustMapPotentialErrors(GlobalPotentialErrorsForQueryParams, groupwareErrorsMap),
		GlobalPotentialErrorsForMandatoryQueryParams: mustMapPotentialErrors(GlobalPotentialErrorsForMandatoryQueryParams, groupwareErrorsMap),
		GlobalPotentialErrorsForPathParams:           mustMapPotentialErrors(GlobalPotentialErrorsForPathParams, groupwareErrorsMap),
		GlobalPotentialErrorsForMandatoryPathParams:  mustMapPotentialErrors(GlobalPotentialErrorsForMandatoryPathParams, groupwareErrorsMap),
		GlobalPotentialErrorsForBodyParams:           mustMapPotentialErrors(GlobalPotentialErrorsForBodyParams, groupwareErrorsMap),
		GlobalPotentialErrorsForMandatoryBodyParams:  mustMapPotentialErrors(GlobalPotentialErrorsForMandatoryBodyParams, groupwareErrorsMap),
	}, nil
}

func mapPotentialErrors(ids []string, errorMap map[string]model.PotentialError) ([]model.PotentialError, error) {
	result := make([]model.PotentialError, len(ids))
	for i, k := range ids {
		if e, ok := errorMap[k]; ok {
			result[i] = e
		} else {
			return nil, fmt.Errorf("failed to find potential error entry '%s' in the groupwareErrorsMap", k)
		}
	}
	return result, nil
}

func mustMapPotentialErrors(ids []string, errorMap map[string]model.PotentialError) []model.PotentialError {
	result, err := mapPotentialErrors(ids, errorMap)
	if err != nil {
		panic(err)
	}
	return result
}

func nameType(t *types.TypeName) string {
	name := t.Name()
	{
		p := t.Pkg()
		if p != nil {
			name = p.Name() + "." + name
		}
	}
	return name
}

func typeNames(t *types.TypeName) (string, string) {
	name := t.Name()
	{
		p := t.Pkg()
		if p != nil {
			return p.Name(), name
		}
	}
	return "", name
}

var (
	arrayRegex = regexp.MustCompile(`^\[\](.+)$`)
	mapRegex   = regexp.MustCompile(`^map\[(.+?)\](.+?)$`)
)

func toType(id string, typeMap map[string]model.Type) (model.Type, error) {
	if m := arrayRegex.FindAllStringSubmatch(id, 1); m != nil {
		if elt, err := toType(m[0][1], typeMap); err != nil {
			return nil, err
		} else {
			return model.NewArrayType(elt), nil
		}
	}
	if m := mapRegex.FindAllStringSubmatch(id, 2); m != nil {
		if key, err := toType(m[0][1], typeMap); err != nil {
			return nil, err
		} else if value, err := toType(m[0][2], typeMap); err != nil {
			return nil, err
		} else {
			return model.NewMapType(key, value), nil
		}
	}
	if t, ok := model.ToBasicType(id); ok {
		return t, nil
	}
	if t, ok := typeMap[id]; ok {
		return t, nil
	}
	return nil, fmt.Errorf("failed to map '%s' to a type", id)
}

func typeOf(t types.Type, mem map[string]model.Type, p *packages.Package) (model.Type, error) {
	switch t := t.(type) {
	case *types.Named:
		pkg, name := typeNames(t.Obj())
		if pkg != "" {
			if model.IsBuiltinSelectorType(pkg, name) { // for things like time.Time
				return model.NewBuiltinType(pkg, name), nil
			}
		} else {
			if model.IsBuiltinType(name) {
				return model.NewBuiltinType(pkg, name), nil
			}
		}

		position := token.Position{}
		pos := token.NoPos
		{
			if t.Obj() != nil {
				pos = t.Obj().Pos()
			}
			if pos != token.NoPos {
				position = p.Fset.Position(pos)
			}
		}

		switch u := t.Underlying().(type) {
		case *types.Basic:
			return model.NewAliasType(pkg, name, model.NewBuiltinType("", u.Name()), position), nil
		case *types.Interface:
			return model.NewInterfaceType(pkg, name, position), nil
		case *types.Map:
			r, err := mapOf(u, mem, p)
			return r, err
		case *types.Array:
			r, err := arrayOf(u.Elem(), mem, p)
			return r, err
		case *types.Slice:
			r, err := arrayOf(u.Elem(), mem, p)
			return r, err
		case *types.Pointer:
			// pointer denotes that it's optional, unless it's explicitly marked as required or not required through annotations or other conventions
			return typeOf(u.Elem(), mem, p)
		case *types.Struct:
			id := fmt.Sprintf("%s.%s", pkg, name)
			if ex, ok := mem[id]; ok {
				return ex, nil
			}

			summary := ""
			description := ""
			if pos != token.NoPos {
				summary, description = summarizeType(findComments(name, pos, token.TYPE, p))
			}

			structFields, err := decompose(name, u, 0, p)
			if err != nil {
				return nil, err
			}

			fields := []model.Field{}
			// add the struct with an empty list of fields into the memory (mem) to avoid
			// endless looping when attempting to resolve the type using typeOf(), in case
			// of circular references
			r := model.NewStructType(pkg, name, fields, summary, description, position)
			mem[id] = r
			{
				name := nameType(t.Obj())
				for _, f := range structFields {
					attr := fieldAttr(f.tag)
					if attr == "-" {
						continue // skip this field
					}
					if attr == "" {
						attr = f.name
					}
					fieldReq := fieldRequirement(f.tag)
					fieldInRequest := fieldInRequest(f.tag)
					fieldInResponse := fieldInResponse(f.tag)
					fieldSummary := strings.Join(findComments(name+"."+f.name, f.pos, token.VAR, p), "\n") // TODO process comments for fields
					if typ, err := typeOf(f.typ, mem, p); err != nil {
						return nil, err
					} else {
						fields = append(fields, model.NewField(f.name, attr, typ, f.tag, fieldSummary, fieldReq, fieldInRequest, fieldInResponse))
					}
				}
			}
			// and now overwrite the struct type with a definition that contains the fields
			r = model.NewStructType(pkg, name, fields, summary, description, position)
			mem[id] = r
			return r, nil
		default:
			return nil, fmt.Errorf("typeOf: unsupported underlying type of named %s.%s is a %T: %#v", pkg, name, u, u)
		}
	case *types.Basic:
		return model.NewBuiltinType("", t.Name()), nil
	case *types.Map:
		return mapOf(t, mem, p)
	case *types.Array:
		return arrayOf(t.Elem(), mem, p)
	case *types.Slice:
		return arrayOf(t.Elem(), mem, p)
	case *types.Pointer:
		return typeOf(t.Elem(), mem, p)
	case *types.Alias:
		return aliasOf(t, mem, p)
	case *types.Interface:
		if t.String() == "any" {
			return model.AnyType, nil
		} else {
			return nil, fmt.Errorf("typeOf: unsupported: using an interface type that isn't any: %T: %#v", t, t)
		}
	case *types.TypeParam:
		// ignore
		return nil, nil
	case *types.Chan:
		// ignore
		return nil, nil
	case *types.Struct:
		// ignore unnamed struct
		return nil, nil
	default:
		return nil, fmt.Errorf("typeOf: unsupported type: %T: %#v", t, t)
	}
}

type field struct {
	name string
	tag  string
	typ  types.Type
	pos  token.Pos
}

func fieldAttr(tag string) string {
	if m := tagAttrNameRegex.FindAllStringSubmatch(tag, 2); m != nil {
		return m[0][1]
	} else {
		return ""
	}
}

var (
	Required    = tools.BoolPtr(true)
	NotRequired = tools.BoolPtr(false)
)

func fieldRequirement(tag string) *bool {
	if m := tagDocRegex.FindAllStringSubmatch(tag, 1); m != nil {
		switch m[0][1] {
		case "req":
			return Required
		case "opt":
			return NotRequired
		case "":
			// noop
		}
	}
	return nil
}

func fieldInRequest(tag string) bool {
	if m := tagNotInRequestRegex.FindAllStringSubmatch(tag, 1); m != nil {
		return false
	}
	return true
}

func fieldInResponse(tag string) bool {
	if m := tagNotInResponseRegex.FindAllStringSubmatch(tag, 1); m != nil {
		return false
	}
	return true
}

func decompose(name string, u *types.Struct, level int, p *packages.Package) ([]field, error) {
	if level > 10 {
		log.Panicf("recursing level %d", level)
	}
	structFields := []field{}
	for i := range u.NumFields() {
		f := u.Field(i)
		switch f.Type().Underlying().(type) {
		case *types.Signature:
			// ignore
		default:
			tag := u.Tag(i)
			if f.Embedded() {
				var c *types.Struct = nil
				switch t := f.Type().(type) {
				case *types.Named:
					switch ut := t.Underlying().(type) {
					case *types.Struct:
						c = ut
					case *types.Interface:
						// ignore
						continue
						// return nil, fmt.Errorf("found an interface")
					default:
						return nil, fmt.Errorf("while typeOf('%s'): (underlying) embedded field is not a struct but a %T", name, ut)
					}
				case *types.Struct:
					c = t
				default:
					return nil, fmt.Errorf("while typeOf('%s'): embedded field is not a struct but a %T", name, u)
				}
				if c == nil {
					return nil, fmt.Errorf("while typeOf('%s'): embedded field is not a struct but a %T", name, u)
				} else {
					if sub, err := decompose(name, c, level+1, p); err != nil { // this could cause an infinite loop
						return nil, err
					} else {
						structFields = append(structFields, sub...)
					}
				}
			} else {
				structFields = append(structFields, field{
					name: f.Name(),
					typ:  f.Type(),
					pos:  f.Pos(),
					tag:  tag,
				})
			}
		}
	}
	return structFields, nil
}

func arrayOf(t types.Type, mem map[string]model.Type, p *packages.Package) (model.Type, error) {
	if e, err := typeOf(t, mem, p); err != nil {
		return nil, err
	} else {
		return model.NewArrayType(e), nil
	}
}

func mapOf(t *types.Map, mem map[string]model.Type, p *packages.Package) (model.Type, error) {
	if k, err := typeOf(t.Key(), mem, p); err != nil {
		return nil, err
	} else {
		if v, err := typeOf(t.Elem(), mem, p); err != nil {
			return nil, err
		} else {
			return model.NewMapType(k, v), nil
		}
	}
}

func aliasOf(t *types.Alias, mem map[string]model.Type, p *packages.Package) (model.Type, error) {
	if e, err := typeOf(t.Underlying(), mem, p); err != nil {
		return nil, err
	} else {
		name, pkg := typeNames(t.Obj())
		return model.NewAliasType(pkg, name, e, p.Fset.Position(t.Obj().Pos())), nil
	}
}
