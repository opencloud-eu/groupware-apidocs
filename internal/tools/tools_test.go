package tools

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSingularize(t *testing.T) {
	for _, tt := range []struct {
		in  string
		out string
	}{
		{"addressbooks", "addressbook"},
		{"blobs", "blob"},
		{"calendars", "calendar"},
		{"events", "event"},
		{"contacts", "contact"},
		{"vacationresponses", "vacationresponse"},
		{"quotas", "quota"},
		{"emails", "email"},
		{"mailboxes", "mailbox"},
	} {
		t.Run(tt.in, func(t *testing.T) {
			require := require.New(t)
			result := Singularize(tt.in)
			require.Equal(tt.out, result)
		})
	}
}

func TestSplice(t *testing.T) {
	for _, tt := range []struct {
		in string
		e1 string
		e2 string
	}{
		{"foo", "foo", ""},
		{"foo:bar", "foo", "bar"},
		{"foo:bar:spam", "foo", "bar:spam"},
		{":bar", "", "bar"},
		{":", "", ""},
		{"", "", ""},
		{":bar:spam", "", "bar:spam"},
	} {
		t.Run(tt.in, func(t *testing.T) {
			require := require.New(t)
			r1, r2 := Splice(tt.in, ":")
			require.Equal([]string{tt.e1, tt.e2}, []string{r1, r2})
		})
	}
}
