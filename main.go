package main

import (
	"flag"
	"io"
	"log"
	"os"
	"path/filepath"

	"opencloud.eu/groupware-apidocs/internal/openapi"
	"opencloud.eu/groupware-apidocs/internal/parser"
)

var (
	verbose                 bool   = false
	defaultOpenIdConnectUrl string = "https://keycloak.opencloud.test/realms/openCloud/.well-known/openid-configuration"
)

func main() {
	chdir := ""
	template := ""
	output := ""
	includeBasicAuth := false
	openIdConnectUrl := defaultOpenIdConnectUrl
	flag.StringVar(&chdir, "C", "", "Change into the specified directory before parsing source files")
	flag.StringVar(&template, "t", "", "Template file")
	flag.StringVar(&output, "o", "", "Output file")
	flag.BoolVar(&includeBasicAuth, "b", false, "Include basic authentication")
	flag.BoolVar(&verbose, "v", false, "Output verbose information while parsing source files")
	flag.StringVar(&openIdConnectUrl, "O", defaultOpenIdConnectUrl, "OIDC URL to reference in the documentation")
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
		o := openapi.NewOpenApiSink(basepath, template, includeBasicAuth, openIdConnectUrl)

		var w io.Writer
		if output == "" || output == "-" {
			w = os.Stdout
		} else {
			if f, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE, 0600); err != nil {
				panic(err)
			} else {
				defer f.Close()
				w = f
			}
		}
		if err := o.Output(model, w); err != nil {
			panic(err)
		}
	}
}
