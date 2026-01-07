package main

import (
	"fmt"
	"io"
	"maps"
	"math"
	"slices"
	"strings"
)

type AnsiSink struct {
}

var _ Sink = AnsiSink{}

func (s AnsiSink) Output(model Model, w io.Writer) error {
	typeMap := map[string]Type{}
	for _, t := range model.Types {
		typeMap[t.Key()] = t
	}
	imMap := map[string]Impl{}
	for _, im := range model.Impls {
		imMap[im.Name] = im
	}

	if verbose {
		fmt.Fprintf(w, "\x1b[4mTypes:\x1b[0m\n")
		for _, t := range model.Types {
			fmt.Fprintf(w, "  \x1b[33m%s\x1b[0m\n", t.Key())
			for _, f := range t.Fields() {
				fmt.Fprintf(w, "    · \x1b[34m%s\x1b[0m %s\n", f.Name, f.Type)
			}
		}
		fmt.Fprintf(w, "\n")
	}

	fmt.Fprintf(w, "\x1b[4mRoutes:\x1b[0m\n")
	for _, r := range model.Routes {
		fmt.Fprintf(w, "\x1b[33m%7.7s\x1b[0m %s \x1b[36m[%s]\x1b[0m", r.Verb, s.hi(r.Path), r.Fun)
		if im, ok := imMap[r.Fun]; ok {
			fmt.Fprintf(w, " \x1b[34m%s\x1b[0m\n", im.Source)
			for _, c := range im.Comments {
				fmt.Fprintf(w, "        \x1b[30;1m# %s\x1b[0m\n", c)
			}
			for _, p := range im.PathParams {
				fmt.Fprintf(w, "        \x1b[35m· /\x1b[0m\x1b[1;35m%s\x1b[0m\n", model.PathParams[p.Name].Name)
			}
			for _, p := range im.QueryParams {
				fmt.Fprintf(w, "        \x1b[34m· ?\x1b[0m\x1b[1;34m%s\x1b[0;34m=\x1b[0m\n", model.QueryParams[p.Name].Name)
			}
			for _, p := range im.HeaderParams {
				fmt.Fprintf(w, "        \x1b[34m· H\x1b[0m\x1b[1;34m%s\x1b[0;34m=\x1b[0m\n", model.HeaderParams[p.Name].Name)
			}
			for _, p := range im.BodyParams {
				fmt.Fprintf(w, "        \x1b[36m· {\x1b[0m")
				if t, ok := typeMap[p]; ok {
					pfx := "          "
					if err := s.printType(w, t, model, 0, pfx, []string{}); err != nil {
						return err
					}
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
				fmt.Fprintf(w, "        \x1b[%sm%d\x1b[0m ", clr, statusCode)
				if resp.Type != nil {
					if err := s.printType(w, resp.Type, model, 0, pfx, []string{}); err != nil {
						return err
					}
				} else {
					fmt.Fprintf(w, "-\n")
				}
			}

		} else {
			return fmt.Errorf(" ❌ not found: %s\n", r.Fun)
		}
		fmt.Fprintf(w, "\n")
	}
	return nil
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

func (s AnsiSink) printType(w io.Writer, t Type, model Model, l int, p string, path []string) error {
	clr := fmt.Sprintf("\x1b[%dm", 31+l)
	pp := p + strings.Repeat("  ", l)
	if l > 10 {
		return fmt.Errorf("level is >10: %d", l)
	}
	recurse := true
	if slices.Contains(path, t.Key()) {
		recurse = false
	}

	switch v := t.(type) {
	case AliasType:
		fmt.Fprintf(w, "%s (%s)", t.Key(), v.typeRef.String())
		if n, ok := model.Enums[t.Key()]; ok {
			fmt.Fprintf(w, " [%s]", strings.Join(n, ","))
		}
		fmt.Fprintf(w, "\n")
	case ArrayType:
		if n, ok := v.Element(); ok {
			fmt.Fprintf(w, "%s%s\x1b[0m", clr, "[]")
			//path = append(path, t.Key())
			if err := s.printType(w, n, model, l+1, p, path); err != nil {
				return err
			}
		} else {
			fmt.Fprintf(w, "%s\n", t.String())
		}
	case StructType:
		if recurse {
			fields := t.Fields()
			if len(fields) < 1 {
				if r, ok := model.resolveType(t.Key()); ok {
					fields = r.Fields()
				}
			}
			fmt.Fprintf(w, "%s %s{\x1b[0m\n", t.String(), clr)
			for _, f := range fields {
				fmt.Fprintf(w, "%s %s-\x1b[0m ", pp, clr)
				if !strings.Contains(f.Type.String(), ".") {
					fmt.Fprintf(w, "\x1b[4m%s\x1b[0m %s\n", s.fieldName(f), s.fieldType(f))
				} else {
					fmt.Fprintf(w, "\x1b[4m%s\x1b[0m ", s.fieldName(f))
					path = append(path, t.Key())
					if err := s.printType(w, f.Type, model, l+1, p, path); err != nil {
						return err
					}
				}
			}
			fmt.Fprintf(w, "%s %s}\x1b[0m\n", pp, clr)
		} else {
			fmt.Fprintf(w, "%s %s↩️\x1b[0m\n", t.String(), clr)
		}
	default:
		fmt.Fprintf(w, "%s\n", t.String())
	}
	return nil
}
