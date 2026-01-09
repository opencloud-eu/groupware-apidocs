package openapi

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestVerbSort(t *testing.T) {
	require := require.New(t)

	require.Zero(verbSort("GET", "GET"))
	require.Negative(verbSort("GET", "POST"))
	require.Positive(verbSort("POST", "GET"))
}
