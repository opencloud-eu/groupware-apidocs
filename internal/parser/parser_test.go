package parser

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSummarizeTypeEmpty(t *testing.T) {
	require := require.New(t)
	s, d := summarizeType([]string{})
	require.Empty(s)
	require.Empty(d)
}

func TestSummarizeTypeOnlySummary(t *testing.T) {
	require := require.New(t)
	s, d := summarizeType([]string{
		"the summary",
	})
	require.Equal("The summary", s)
	require.Empty(d)
}

func TestSummarizeTypeOnlySummaryWithEmptyLines(t *testing.T) {
	require := require.New(t)
	s, d := summarizeType([]string{
		"",
		"",
		"the summary",
		"",
	})
	require.Equal("The summary", s)
	require.Empty(d)
}

func TestSummarizeTypeWithEmptyLines(t *testing.T) {
	require := require.New(t)
	s, d := summarizeType([]string{
		"",
		"",
		"the summary",
		"",
		"more is described here",
		"technically one could say it's the description.",
	})
	require.Equal("The summary", s)
	require.Equal("More is described here\ntechnically one could say it's the description.", d)
}

func TestProcessComments(t *testing.T) {
	require := require.New(t)
	{
		result := processComments([]string{"hello [RFC123] this is a test"})
		require.Len(result, 1)
		require.Equal("hello [RFC123](https://www.rfc-editor.org/rfc/rfc123.html) this is a test", result[0])
	}
	{
		result := processComments([]string{"hello [RFC123](with a link) this is a test"})
		require.Len(result, 1)
		require.Equal("hello [RFC123](with a link) this is a test", result[0])
	}
	{
		result := processComments([]string{"hello [RFC123]"})
		require.Len(result, 1)
		require.Equal("hello [RFC123](https://www.rfc-editor.org/rfc/rfc123.html)", result[0])
	}
}
