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
