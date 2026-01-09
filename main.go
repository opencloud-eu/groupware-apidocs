package main

import (
	"flag"
	"log"
	"os"
	"path/filepath"

	"opencloud.eu/groupware-apidocs/internal/openapi"
	"opencloud.eu/groupware-apidocs/internal/parser"
)

var verbose bool = false

func main() {
	chdir := ""
	template := ""
	flag.StringVar(&chdir, "C", "", "Change into the specified directory before parsing source files")
	flag.StringVar(&template, "t", "", "Template file")
	flag.BoolVar(&verbose, "v", false, "Output verbose information while parsing source files")
	flag.Parse()

	var err error
	basepath := ""
	if chdir != "" {
		basepath, err = filepath.Abs(chdir)
		if err != nil {
			log.Fatalf("failed to make an absolute path out of '%s': %v", chdir, err)
		}
	} else {
		basepath, err = os.Getwd()
		if err != nil {
			log.Fatalf("failed to get current working directory: %v", err)
		}
		basepath, err = filepath.Abs(basepath)
		if err != nil {
			log.Fatalf("failed to make an absolute path out of the current working directory '%s': %v", chdir, err)
		}
	}

	if model, err := parser.Parse(chdir, basepath); err != nil {
		panic(err)
	} else {
		//o := AnsiSink{Verbose: verbose}
		o := openapi.OpenApiSink{
			TemplateFile:     template,
			IncludeBasicAuth: false,
			BasePath:         basepath,
		}
		if err := o.Output(model, os.Stdout); err != nil {
			panic(err)
		}
	}
}
