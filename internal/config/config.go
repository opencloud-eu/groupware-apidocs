package config

var PackagesOfInterest = []string{
	"groupware",
	"jmap",
	"jscontact",
	"jscalendar",
}

const (
	GroupwarePackageID  = "github.com/opencloud-eu/opencloud/services/groupware/pkg/groupware"
	JmapPackageID       = "github.com/opencloud-eu/opencloud/pkg/jmap"
	JSCalendarPackageID = "github.com/opencloud-eu/opencloud/pkg/jscalendar"
	JSContactPacakgeID  = "github.com/opencloud-eu/opencloud/pkg/jscontact"

	HttpPackageID = "net/http"
)

var SourceDirectories = []string{
	"./services/groupware/pkg/groupware",
	"./pkg/jmap",
	"./pkg/jscontact",
	"./pkg/jscalendar",
}

var PackageIDs = []string{
	GroupwarePackageID,
	JmapPackageID,
	JSCalendarPackageID,
	JSContactPacakgeID,
}

var MiddlewareFunctionNames = []string{
	"ServeHTTP",
	"ServeSSE",
	"NotFound",
	"MethodNotAllowed",
}

var (
	QueryParamPrefixes = []string{
		"QueryParam",
	}
	PathParamPrefixes = []string{
		"UriParam",
		"PathParam",
	}
	HeaderParamPrefixes = []string{
		"HeaderParam",
	}
)

var (
	AccountIdUriParamName = "UriParamAccountId"
)

var Verbs = []string{
	"Get",
	"Put",
	"Post",
	"Delete",
	"Patch",
}

var CustomVerbs = []string{
	"Report",
}
