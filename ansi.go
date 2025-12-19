package main

import (
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
)

type AnsiSink struct {
}

var _ Sink = AnsiSink{}

func (s AnsiSink) Output(model Model) {
	typeMap := map[string]Type{}
	for _, t := range model.Types {
		typeMap[t.Key()] = t
	}
	imMap := map[string]Impl{}
	for _, im := range model.Impls {
		imMap[im.Name] = im
	}

	if verbose {
		fmt.Printf("\x1b[4mTypes:\x1b[0m\n")
		for _, t := range model.Types {
			fmt.Printf("  \x1b[33m%s\x1b[0m\n", t.Key())
			for _, f := range t.Fields() {
				fmt.Printf("    · \x1b[34m%s\x1b[0m %s\n", f.Name, f.Type)
			}
		}
		fmt.Println()
	}

	fmt.Printf("\x1b[4mRoutes:\x1b[0m\n")
	for _, r := range model.Routes {
		fmt.Printf("\x1b[33m%7.7s\x1b[0m %s \x1b[36m[%s]\x1b[0m", r.Verb, s.hi(r.Path), r.Fun)
		if im, ok := imMap[r.Fun]; ok {
			fmt.Printf(" \x1b[34m%s\x1b[0m\n", im.Source)
			for _, c := range im.Comments {
				fmt.Printf("        \x1b[30;1m# %s\x1b[0m\n", c)
			}
			for _, p := range im.UriParams {
				fmt.Printf("        \x1b[35m· /\x1b[0m\x1b[1;35m%s\x1b[0m\n", model.UriParams[p])
			}
			for _, p := range im.QueryParams {
				fmt.Printf("        \x1b[34m· ?\x1b[0m\x1b[1;34m%s\x1b[0;34m=\x1b[0m\n", model.QueryParams[p])
			}
			for _, p := range im.BodyParams {
				fmt.Printf("        \x1b[36m· {\x1b[0m")
				if t, ok := typeMap[p]; ok {
					pfx := "          "
					s.printType(t, model, 0, pfx, []string{})
				}
			}

			resps := map[int]Resp{}
			for statusCode, t := range model.DefaultResponses {
				resps[statusCode] = Resp{Type: t}
			}
			maps.Copy(resps, im.Resp)

			statusCodes := slices.Collect(maps.Keys(resps))
			slices.Sort(statusCodes)

			for _, statusCode := range statusCodes {
				resp := resps[statusCode]
				clr := "31;1"
				if statusCode < 300 {
					clr = "32;1"
				}
				pfx := "        " + strings.Repeat(" ", int(math.Log10(float64(statusCode)))+1) + "  "
				fmt.Printf("        \x1b[%sm%d\x1b[0m ", clr, statusCode)
				if resp.Type != nil {
					s.printType(resp.Type, model, 0, pfx, []string{})
				} else {
					fmt.Printf("-\n")
				}
			}

		} else {
			panic(fmt.Sprintf(" ❌ not found: %s\n", r.Fun))
		}
		fmt.Println()
	}
}

func (s AnsiSink) hi(path string) string {
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

func (s AnsiSink) fieldName(field Field) string {
	return field.Attr
}

func (s AnsiSink) fieldType(field Field) string {
	return field.Type.String()
}

func (s AnsiSink) printType(t Type, model Model, l int, p string, path []string) {
	clr := fmt.Sprintf("\x1b[%dm", 31+l)
	pp := p + strings.Repeat("  ", l)
	if l > 10 {
		panic("level is >5")
	}
	recurse := true
	if slices.Contains(path, t.Key()) {
		recurse = false
	}

	switch v := t.(type) {
	case AliasType:
		fmt.Printf("%s (%s)", t.Key(), v.typeRef.String())
		if n, ok := model.Enums[t.Key()]; ok {
			fmt.Printf(" [%s]", strings.Join(n, ","))
		}
		fmt.Printf("\n")
	case ArrayType:
		if n, ok := v.Element(); ok {
			fmt.Printf("%s%s\x1b[0m", clr, "[]")
			//path = append(path, t.Key())
			s.printType(n, model, l+1, p, path)
		} else {
			fmt.Printf("%s\n", t.String())
		}
	case StructType:
		if recurse {
			fields := t.Fields()
			if len(fields) < 1 {
				if r, ok := model.resolveType(t.Key()); ok {
					fields = r.Fields()
				}
			}
			fmt.Printf("%s %s{\x1b[0m\n", t.String(), clr)
			for _, f := range fields {
				fmt.Printf("%s %s-\x1b[0m ", pp, clr)
				if !strings.Contains(f.Type.String(), ".") {
					fmt.Printf("\x1b[4m%s\x1b[0m %s\n", s.fieldName(f), s.fieldType(f))
				} else {
					fmt.Printf("\x1b[4m%s\x1b[0m ", s.fieldName(f))
					path = append(path, t.Key())
					s.printType(f.Type, model, l+1, p, path)
				}
			}
			fmt.Printf("%s %s}\x1b[0m\n", pp, clr)
		} else {
			fmt.Printf("%s %s↩️\x1b[0m\n", t.String(), clr)
		}
	default:
		fmt.Printf("%s\n", t.String())
	}
}
