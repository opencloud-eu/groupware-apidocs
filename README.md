# OpenCloud Groupware API Documentation Generator

The purpose of this tool is to analyze the OpenCloud Groupware backend source code to extract
information about the REST APIs it exposes, and to render those into [OpenAPI](https://swagger.io/specification/).

The motivation for implementing a piece of custom software to do so is to avoid repetition and
other error prone practices that are required by the use of a generic solution such as [go-swagger](https://github.com/go-swagger/go-swagger),
and instead making use of the conventions as well as the understanding of the underlying framework
implemented in the Groupware backend in order to minimize annotation efforts.

## Usage

Build `groupware-apidocs` and put it somewhere in your `PATH`:

```bash
go build
ln -s "$PWD/groupware-apidocs" ~/.local/bin/
```

Then execute the following make target in the OpenCloud source tree:

```bash
make -C ./services/groupware/ api.html
```

That will create a Redoc bundle of the Groupware OpenAPI, which you can then enjoy using your favourite web browser:

```bash
firefox ./services/groupware/api.html
```


