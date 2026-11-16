package common

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestModelFacingIDMatchesOfficialV1Vectors(t *testing.T) {
	const seed = uint64(0x123456789abcdef0)
	for ordinal, expected := range []string{"895", "930", "155", "141", "916", "912", "678", "430"} {
		id, err := ModelFacingIDFromState(1, seed, "environment", uint64(ordinal))
		require.NoError(t, err)
		require.Equal(t, "environment"+expected, id.String())
	}
	for _, vector := range []struct {
		ordinal  uint64
		expected string
	}{
		{1605, "765577"}, {999, "553"}, {1000, "661326"}, {999999, "753874"}, {1000000, "286136338"},
		{999999999, "839882170"}, {1000000000, "813648058018"},
		{999999999999, "634462456979"}, {1000000000000, "134726697070558"},
		{999999999999999, "326088534089890"}, {1000000000000000, "563366069705075074"},
		{999999999999999999, "633936232025849076"}, {1000000000000000000, "012827849051678362461"},
	} {
		salt := "env"
		if vector.ordinal == 1605 {
			salt = "environment"
		}
		id, err := ModelFacingIDFromState(1, seed, salt, vector.ordinal)
		require.NoError(t, err)
		require.Equal(t, salt+vector.expected, id.String())
	}
	id, err := ModelFacingIDAt(42, "environment", 1000)
	require.NoError(t, err)
	require.Equal(t, "environment825109", id.String())
	id, err = ModelFacingIDFromState(1, 17, "env", math.MaxUint64)
	require.NoError(t, err)
	require.GreaterOrEqual(t, id.Value, uint64(1000000000000000000))
	_, err = ModelFacingIDFromState(2, seed, "window", 0)
	require.Error(t, err)
}
